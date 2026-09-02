// h2_hook5.js - 全面 hook + WSABUF 正确解析
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

function logLine(tag, dir, sock, len, bufPtr) {
    var line = "[" + ts() + "] " + dir + " sock=" + sock + " len=" + len + " tag=" + tag;
    logFile.write(line + "\n");
    if (len > 0 && bufPtr) {
        logFile.write(myhex(bufPtr, len, 96) + "\n");
    }
    logFile.flush();
}

function getModule(name) {
    try { return Process.getModuleByName(name); } catch (e) { return null; }
}

// WSABUF: 32 位 -> len@0 (4B), buf@4 (4B); 64 位 -> len@0 (4B), pad@4, buf@8 (8B)
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

// 简单 buffer 型 hook：send/recv/sendto/recvfrom (sock, buf, len, flags)
function hookBuf(mod, name, dir) {
    var m = getModule(mod);
    if (!m) return;
    var p;
    try { p = m.getExportByName(name); } catch (e) { p = null; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            logLine(mod + "!" + name, dir, args[0].toInt32(), args[2].toInt32(), args[1]);
        },
        onLeave: function () {}
    });
    logFile.write("[" + ts() + "] HOOKED " + mod + "!" + name + " @ " + p + "\n");
    logFile.flush();
}

// WSABUF 数组型 hook：WSASend/WSARecv/WSASendTo/WSARecvFrom
// WSASend(s, lpBuffers, dwBufferCount, ...)
// WSASendTo(s, lpBuffers, dwBufferCount, ..., to, tolen, ...)
function hookWsa(mod, name, dir) {
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
                // 记录 WSABUF[0] 的 len 和 buf 数据
                var line = "[" + ts() + "] " + dir + " sock=" + sock + " tag=" + mod + "!" + name +
                           " bufCount=" + cnt + " WSABUF.len=" + w.len;
                logFile.write(line + "\n");
                if (w.len > 0 && w.buf) {
                    logFile.write(myhex(w.buf, w.len, 96) + "\n");
                }
            } catch (e) {
                logFile.write("[" + ts() + "] " + dir + " sock=" + sock + " tag=" + mod + "!" + name +
                              " wsa parse fail: " + e + "\n");
            }
            logFile.flush();
        },
        onLeave: function () {}
    });
    logFile.write("[" + ts() + "] HOOKED " + mod + "!" + name + " @ " + p + "\n");
    logFile.flush();
}

// 基础 buffer API
hookBuf("ws2_32.dll", "send", "SEND");
hookBuf("ws2_32.dll", "sendto", "SEND");
hookBuf("ws2_32.dll", "recv", "RECV");
hookBuf("ws2_32.dll", "recvfrom", "RECV");
hookBuf("WSOCK32.dll", "send", "SEND");
hookBuf("WSOCK32.dll", "sendto", "SEND");
hookBuf("WSOCK32.dll", "recv", "RECV");
hookBuf("WSOCK32.dll", "recvfrom", "RECV");

// WSABUF 型 API
hookWsa("ws2_32.dll", "WSASend", "SEND");
hookWsa("ws2_32.dll", "WSARecv", "RECV");
hookWsa("ws2_32.dll", "WSASendTo", "SEND");
hookWsa("ws2_32.dll", "WSARecvFrom", "RECV");

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
                fam = (b[0] << 8) | b[1];
                port = (b[2] << 8) | b[3];
                if (fam === 2) {
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

logFile.write("[" + ts() + "] hook5 installed\n");
logFile.flush();
console.log("[h2hook5] installed on " + Process.id);
