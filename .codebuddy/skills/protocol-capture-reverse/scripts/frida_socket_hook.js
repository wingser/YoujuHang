// frida_socket_hook.js — Windows 目标进程的 socket 收发 hook 模板
//
// 用途：当流量走 TLS/自定义加密、Wireshark 只能看到密文时，
//       在 send/recv 层截获明文，补 pcap 的不足。
//
// 关键设计（踩过的坑，勿改）：
//   1. send 系列在 onEnter 读 —— 数据在 WSABUF/缓冲区里，此时有效
//   2. recv 系列在 onLeave 读 —— 只有返回后才知道实际收到多少字节；
//      读 ret.toInt32()，WSARecv 还要优先读 lpNumberOfBytesRecvd
//   3. WSABUF 结构在 64 位是 {u_long len; char* buf}（8 字节对齐，指针在 +8），
//      32 位指针在 +4；按 Process.pointerSize 区分
//   4. 每次 write 后必须 flush，否则进程被杀时日志丢失
//
// 注入方式（配合 Python frida 包）：
//   js = open('frida_socket_hook.js').read().replace('%OUTPUT%', 'out.log')
//   session = frida.attach('X-Zone.exe')
//   script = session.create_script(js)
//   script.load()
//   sys.stdin.read()      # 保持存活
//   session.detach()

var outPath = "%OUTPUT%";
var logFile = new File(outPath, "w");
var MAXHEX = 4096;

function ts() { return Date.now() % 100000000; }

function myhex(ptr, len, max) {
    var n = Math.min(len, max);
    try {
        var bytes = ptr.readByteArray(n);
        if (!bytes) return "<read fail>";
        var arr = new Uint8Array(bytes);
        var out = "";
        for (var i = 0; i < n; i++) {
            out += ("0" + arr[i].toString(16)).slice(-2) + " ";
            if (i % 16 === 15) out += "\n";
        }
        if (len > max) out += "... (" + len + " bytes total)";
        return out;
    } catch (e) {
        return "<read fail " + e + ">";
    }
}

function logHex(line, bufPtr, len) {
    logFile.write(line + "\n");
    if (len > 0 && bufPtr) logFile.write(myhex(bufPtr, len, MAXHEX) + "\n");
    logFile.flush();
}

function note(msg) {
    logFile.write("[" + ts() + "] " + msg + "\n");
    logFile.flush();
}

function getModule(name) {
    try { return Process.getModuleByName(name); } catch (e) { return null; }
}

// WSABUF: 64位 = {u_long len(4) + pad(4), void* buf}; 32位 = {u_long len(4), void* buf}
function wsabufData(p, is64) {
    var len = p.readU32();
    var buf = is64 ? p.add(8).readPointer() : p.add(4).readPointer();
    return { len: len, buf: buf };
}

var is64 = Process.pointerSize === 8;

function hookSend(mod, name) {
    var m = getModule(mod); if (!m) return;
    var p; try { p = m.getExportByName(name); } catch (e) { return; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            logHex("[" + ts() + "] SEND sock=" + args[0].toInt32() +
                   " len=" + args[2].toInt32() + " tag=" + mod + "!" + name,
                   args[1], args[2].toInt32());
        }
    });
    note("HOOKED " + mod + "!" + name);
}

function hookRecv(mod, name) {
    var m = getModule(mod); if (!m) return;
    var p; try { p = m.getExportByName(name); } catch (e) { return; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            this.sock = args[0].toInt32();
            this.buf = args[1];
        },
        onLeave: function (ret) {
            var n = ret.toInt32();
            if (n > 0) {
                logHex("[" + ts() + "] RECV sock=" + this.sock + " len=" + n +
                       " tag=" + mod + "!" + name, this.buf, n);
            }
        }
    });
    note("HOOKED " + mod + "!" + name);
}

function hookWsaSend(mod, name) {
    var m = getModule(mod); if (!m) return;
    var p; try { p = m.getExportByName(name); } catch (e) { return; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            var sock = args[0].toInt32();
            var cnt = args[2].toInt32();
            try {
                var w = wsabufData(args[1], is64);
                logHex("[" + ts() + "] SEND sock=" + sock + " tag=" + mod + "!" + name +
                       " bufCount=" + cnt + " WSABUF.len=" + w.len, w.buf, w.len);
            } catch (e) {
                note("SEND sock=" + sock + " " + name + " wsa fail: " + e);
            }
        }
    });
    note("HOOKED " + mod + "!" + name);
}

function hookWsaRecv(mod, name) {
    var m = getModule(mod); if (!m) return;
    var p; try { p = m.getExportByName(name); } catch (e) { return; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            this.sock = args[0].toInt32();
            this.wsaBuf = args[1];
            this.cnt = args[2].toInt32();
            this.lpNumber = args[3];
        },
        onLeave: function (ret) {
            var n = 0;
            try {
                if (this.lpNumber && !this.lpNumber.isNull()) n = this.lpNumber.readS32();
            } catch (e) {}
            if (n <= 0) n = ret.toInt32();
            if (n > 0) {
                try {
                    var w = wsabufData(this.wsaBuf, is64);
                    logHex("[" + ts() + "] RECV sock=" + this.sock + " tag=" + mod + "!" + name +
                           " WSABUF.len=" + w.len + " recvd=" + n, w.buf, Math.min(n, w.len));
                } catch (e) {}
            }
            logFile.flush();
        }
    });
    note("HOOKED " + mod + "!" + name);
}

// 基础 send/recv
["send", "sendto"].forEach(function (n) { hookSend("ws2_32.dll", n); hookSend("WSOCK32.dll", n); });
["recv", "recvfrom"].forEach(function (n) { hookRecv("ws2_32.dll", n); hookRecv("WSOCK32.dll", n); });
// WSABUF 型
hookWsaSend("ws2_32.dll", "WSASend");
hookWsaSend("ws2_32.dll", "WSASendTo");
hookWsaRecv("ws2_32.dll", "WSARecv");
hookWsaRecv("ws2_32.dll", "WSARecvFrom");

// connect：把 socket 号映射到目标 ip:port，便于识别流量归属
try {
    var c = getModule("ws2_32.dll").getExportByName("connect");
    Interceptor.attach(c, {
        onEnter: function (args) {
            var sock = args[0].toInt32();
            var sa = args[1];
            var info = "";
            try {
                var b = new Uint8Array(sa.readByteArray(16));
                var fam = (b[1] << 8) | b[0];
                var port = (b[2] << 8) | b[3];
                if (fam === 2) info = b[4] + "." + b[5] + "." + b[6] + "." + b[7] + ":" + port;
                else info = "fam=" + fam + " port=" + port;
            } catch (e) { info = "<parse fail>"; }
            note("CONNECT sock=" + sock + " " + info);
        }
    });
    note("HOOKED connect");
} catch (e) {
    note("connect hook fail: " + e);
}

note("frida_socket_hook installed (pid=" + Process.id + ")");
console.log("[frida_socket_hook] installed, writing to " + outPath);
