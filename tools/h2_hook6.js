// h2_hook6.js - 修复版：RECV 在 onLeave 读取实际数据，SEND 保留 onEnter
// - send/sendto/WSASend/WSASendTo: onEnter 记录（数据在 buffer）
// - recv/recvfrom/WSARecv/WSARecvFrom: onLeave 记录（返回值/recvd 为实际字节）
// - myhex 上限 4096，SEND/RECV 均完整显示小消息
var outPath = "%OUTPUT%";
var logFile = new File(outPath, "w");

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
    if (len > 0 && bufPtr) {
        logFile.write(myhex(bufPtr, len, 4096) + "\n");
    }
    logFile.flush();
}

function getModule(name) {
    try { return Process.getModuleByName(name); } catch (e) { return null; }
}

function wsabufData(wsabufPtr, is64) {
    var b = new Uint8Array(wsabufPtr.readByteArray(16));
    var len, buf;
    if (is64) {
        len = (b[0] | b[1] << 8 | b[2] << 16 | b[3] << 24) >>> 0;
        buf = wsabufPtr.add(8).readPointer();
    } else {
        len = (b[0] | b[1] << 8 | b[2] << 16 | b[3] << 24) >>> 0;
        buf = wsabufPtr.add(4).readPointer();
    }
    return { len: len, buf: buf };
}

var is64 = Process.pointerSize === 8;

// send 系列（onEnter 记录）
function hookSend(mod, name) {
    var m = getModule(mod);
    if (!m) return;
    var p;
    try { p = m.getExportByName(name); } catch (e) { p = null; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            logHex("[" + ts() + "] SEND sock=" + args[0].toInt32() + " len=" + args[2].toInt32() + " tag=" + mod + "!" + name,
                   args[1], args[2].toInt32());
        },
        onLeave: function () {}
    });
    logFile.write("[" + ts() + "] HOOKED " + mod + "!" + name + "\n");
    logFile.flush();
}

// recv 系列（onLeave 记录）
function hookRecv(mod, name) {
    var m = getModule(mod);
    if (!m) return;
    var p;
    try { p = m.getExportByName(name); } catch (e) { p = null; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            this.sock = args[0].toInt32();
            this.buf = args[1];
        },
        onLeave: function (ret) {
            var n = ret.toInt32();
            if (n > 0) {
                logHex("[" + ts() + "] RECV sock=" + this.sock + " len=" + n + " tag=" + mod + "!" + name,
                       this.buf, n);
            }
        }
    });
    logFile.write("[" + ts() + "] HOOKED " + mod + "!" + name + "\n");
    logFile.flush();
}

// WSASend（onEnter）
function hookWsaSend(mod, name) {
    var m = getModule(mod);
    if (!m) return;
    var p;
    try { p = m.getExportByName(name); } catch (e) { p = null; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            var sock = args[0].toInt32();
            var wsaBuf = args[1];
            var cnt = args[2].toInt32();
            try {
                var w = wsabufData(wsaBuf, is64);
                logHex("[" + ts() + "] SEND sock=" + sock + " tag=" + mod + "!" + name +
                       " bufCount=" + cnt + " WSABUF.len=" + w.len, w.buf, w.len);
            } catch (e) {
                logFile.write("[" + ts() + "] SEND sock=" + sock + " tag=" + mod + "!" + name + " wsa fail: " + e + "\n");
            }
            logFile.flush();
        },
        onLeave: function () {}
    });
    logFile.write("[" + ts() + "] HOOKED " + mod + "!" + name + "\n");
    logFile.flush();
}

// WSARecv/WSARecvFrom（onLeave 读 lpNumberOfBytesRecvd，fallback onEnter 残留）
function hookWsaRecv(mod, name) {
    var m = getModule(mod);
    if (!m) return;
    var p;
    try { p = m.getExportByName(name); } catch (e) { p = null; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            this.sock = args[0].toInt32();
            this.wsaBuf = args[1];
            this.cnt = args[2].toInt32();
            this.lpNumber = args[3];
            // fallback: 读 buffer 当前内容（可能为上次异步数据）
            try {
                var w = wsabufData(this.wsaBuf, is64);
                if (w.len > 0) {
                    // 读第一个字节判断是否有残留（非全零）
                    var probe = w.buf.readU8();
                    if (probe !== 0) {
                        logHex("[" + ts() + "] RECVB sock=" + this.sock + " tag=" + mod + "!" + name +
                               " bufCount=" + this.cnt + " WSABUF.len=" + w.len + " (stale)", w.buf, w.len);
                    }
                }
            } catch (e) {}
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
                           " bufCount=" + this.cnt + " WSABUF.len=" + w.len + " recvd=" + n, w.buf, Math.min(n, w.len));
                } catch (e) {}
            }
            logFile.flush();
        }
    });
    logFile.write("[" + ts() + "] HOOKED " + mod + "!" + name + "\n");
    logFile.flush();
}

// send/recv 基础 API
hookSend("ws2_32.dll", "send");
hookSend("ws2_32.dll", "sendto");
hookRecv("ws2_32.dll", "recv");
hookRecv("ws2_32.dll", "recvfrom");
hookSend("WSOCK32.dll", "send");
hookSend("WSOCK32.dll", "sendto");
hookRecv("WSOCK32.dll", "recv");
hookRecv("WSOCK32.dll", "recvfrom");

// WSABUF 型 API
hookWsaSend("ws2_32.dll", "WSASend");
hookWsaSend("ws2_32.dll", "WSASendTo");
hookWsaRecv("ws2_32.dll", "WSARecv");
hookWsaRecv("ws2_32.dll", "WSARecvFrom");

// connect
try {
    var ws2 = getModule("ws2_32.dll");
    var c = ws2.getExportByName("connect");
    Interceptor.attach(c, {
        onEnter: function (args) {
            var sock = args[0].toInt32();
            var sa = args[1];
            var fam = -1, port = -1, ip = "";
            try {
                var b = new Uint8Array(sa.readByteArray(16));
                var f = (b[1] << 8) | b[0];
                fam = f;
                port = (b[2] << 8) | b[3];
                if (f === 2) {
                    ip = b[4] + "." + b[5] + "." + b[6] + "." + b[7];
                }
            } catch (e) {}
            logFile.write("[" + ts() + "] CONNECT sock=" + sock + " fam=" + fam + " " + ip + ":" + port + "\n");
            logFile.flush();
        },
        onLeave: function () {}
    });
    logFile.write("[" + ts() + "] HOOKED connect\n");
    logFile.flush();
} catch (e) {
    logFile.write("[" + ts() + "] connect hook fail: " + e + "\n");
    logFile.flush();
}

logFile.write("[" + ts() + "] hook6 installed\n");
logFile.flush();
console.log("[h2hook6] installed on " + Process.id);
