// h2_hook.js - hook ws2_32 send/recv/WSASend/WSARecv/connect
// 捕获 HTTP/2 帧 + HEADERS 密文 + 调用栈返回地址
// 输出到 C:\Users\Lenovo\AppData\Local\Temp\h2hook.log (由 Python 传入路径)

var outPath = "%OUTPUT%";
var logFile = new File(outPath, "w");

function ts() {
    return Date.now() % 100000000;
}

function myhex(ptr, len, max) {
    var n = Math.min(len, max);
    var bytes = Memory.readByteArray(ptr, n);
    if (!bytes) return "<read fail>";
    var arr = new Uint8Array(bytes);
    var out = "";
    for (var i = 0; i < n; i++) {
        out += ("0" + arr[i].toString(16)).slice(-2) + " ";
        if (i % 16 === 15) out += "\n";
    }
    if (len > max) out += "... (" + len + " bytes total)";
    return out;
}

function looksHttp2(buf) {
    // 判断是否 HTTP/2 帧：PRI * 前言，或 帧头 length<0x4000
    try {
        var b = new Uint8Array(buf);
        if (b.length >= 24 && b[0] === 0x50 && b[1] === 0x52 && b[2] === 0x49) return true;
        if (b.length >= 3) {
            var len = (b[0] << 16) | (b[1] << 8) | b[2];
            if (len < 0x4000 && len <= b.length) return true;
        }
    } catch (e) {}
    return false;
}

function logLine(tag, dir, sock, len, bufPtr, backtrace) {
    var line = "[" + ts() + "] " + dir + " sock=" + sock + " len=" + len + " tag=" + tag;
    if (backtrace) {
        line += " bt=" + backtrace.map(function (a) { return "0x" + a.toString(16); }).join(",");
    }
    logFile.write(line + "\n");
    if (len > 0 && bufPtr) {
        logFile.write(myhex(bufPtr, len, 256) + "\n");
    }
    logFile.flush();
}

var ws2 = Process.getModuleByName("ws2_32.dll");

function getExport(name) {
    try {
        return ws2.getExportByName(name);
    } catch (e) {
        return null;
    }
}

function hookSend(name) {
    var p = getExport(name);
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            this.sock = args[0].toInt32();
            this.buf = args[1];
            this.len = args[2].toInt32();
        },
        onLeave: function (ret) {
            var n = ret.toInt32();
            if (n > 0 && this.len > 0) {
                var buf = this.buf;
                var bt = Thread.backtrace(this.context, Backtracer.ACCURATE).slice(0, 8);
                var tag = looksHttp2(buf) ? "H2" : "TCP";
                logLine(tag, "SEND", this.sock, n, buf, bt);
            }
        }
    });
}

function hookRecv(name) {
    var p = getExport(name);
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            this.sock = args[0].toInt32();
            this.buf = args[1];
            this.len = args[2].toInt32();
        },
        onLeave: function (ret) {
            var n = ret.toInt32();
            if (n > 0 && this.len > 0) {
                var buf = this.buf;
                var bt = Thread.backtrace(this.context, Backtracer.ACCURATE).slice(0, 8);
                var tag = looksHttp2(buf) ? "H2" : "TCP";
                logLine(tag, "RECV", this.sock, n, buf, bt);
            }
        }
    });
}

// connect: 记录远程地址，映射 sock -> ip:port
var sockMap = {};

var connectP = getExport("connect");
if (connectP) {
    Interceptor.attach(connectP, {
        onEnter: function (args) {
            this.sock = args[0].toInt32();
            this.addr = args[1];
        },
        onLeave: function (ret) {
            if (ret.toInt32() === 0) {
                try {
                    var fam = Memory.readU16(this.addr);
                    var port = Memory.readU16(this.addr.add(2));
                    var ip = Memory.readAnsiString(this.addr.add(4));
                    sockMap[this.sock] = ip + ":" + port;
                    logLine("CONNECT", "OUT", this.sock, 0, null, null);
                    logFile.write("  -> " + ip + ":" + port + "\n");
                    logFile.flush();
                } catch (e) {}
            }
        }
    });
}

// 每 3 秒清理未知 socket 记录，输出当前连接的 sock 映射
setInterval(function () {
    var keys = Object.keys(sockMap);
    if (keys.length) {
        var line = "[" + ts() + "] SOCKMAP " + keys.map(function (k) { return k + "=" + sockMap[k]; }).join(" ");
        logFile.write(line + "\n");
        logFile.flush();
    }
}, 3000);

hookSend("send");
hookSend("WSASend");
hookRecv("recv");
hookRecv("WSARecv");

logFile.write("[" + ts() + "] hook installed\n");
logFile.flush();

console.log("[h2hook] installed on " + Process.id);
