// h2_hook4.js - 全面 hook：ws2_32 + WSOCK32 的 send/recv 系列 + connect
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

function hookSockSend(mod, name) {
    var m = getModule(mod);
    if (!m) return;
    var p;
    try { p = m.getExportByName(name); } catch (e) { p = null; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            logLine(mod + "!" + name, "SEND", args[0].toInt32(), args[2].toInt32(), args[1]);
        },
        onLeave: function () {}
    });
    logFile.write("[" + ts() + "] HOOKED " + mod + "!" + name + " @ " + p + "\n");
    logFile.flush();
}

function hookSockRecv(mod, name) {
    var m = getModule(mod);
    if (!m) return;
    var p;
    try { p = m.getExportByName(name); } catch (e) { p = null; }
    if (!p) return;
    Interceptor.attach(p, {
        onEnter: function (args) {
            logLine(mod + "!" + name, "RECV", args[0].toInt32(), args[2].toInt32(), args[1]);
        },
        onLeave: function () {}
    });
    logFile.write("[" + ts() + "] HOOKED " + mod + "!" + name + " @ " + p + "\n");
    logFile.flush();
}

// ws2_32
hookSockSend("ws2_32.dll", "send");
hookSockSend("ws2_32.dll", "WSASend");
hookSockSend("ws2_32.dll", "sendto");
hookSockSend("ws2_32.dll", "WSASendTo");
hookSockRecv("ws2_32.dll", "recv");
hookSockRecv("ws2_32.dll", "WSARecv");
hookSockRecv("ws2_32.dll", "recvfrom");
hookSockRecv("ws2_32.dll", "WSARecvFrom");

// WSOCK32 (1.1)
hookSockSend("WSOCK32.dll", "send");
hookSockSend("WSOCK32.dll", "sendto");
hookSockSend("WSOCK32.dll", "WSASend");
hookSockRecv("WSOCK32.dll", "recv");
hookSockRecv("WSOCK32.dll", "recvfrom");
hookSockRecv("WSOCK32.dll", "WSARecv");

// connect (ws2_32)
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

logFile.write("[" + ts() + "] hook4 installed\n");
logFile.flush();
console.log("[h2hook4] installed on " + Process.id);
