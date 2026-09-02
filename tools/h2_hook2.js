// h2_hook2.js - 增强版：hook send/recv/WSASend/WSARecv/sendto/WSARecvFrom
// onEnter 直接记录，避免 onLeave 依赖
var outPath = "%OUTPUT%";
var logFile = new File(outPath, "w");

function ts() { return Date.now() % 100000000; }

function myhex(ptr, len, max) {
    var n = Math.min(len, max);
    try {
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
    } catch (e) {
        return "<read fail " + e + ">";
    }
}

function logLine(tag, dir, sock, len, bufPtr) {
    var line = "[" + ts() + "] " + dir + " sock=" + sock + " len=" + len + " tag=" + tag;
    logFile.write(line + "\n");
    if (len > 0 && bufPtr) {
        logFile.write(myhex(bufPtr, len, 128) + "\n");
    }
    logFile.flush();
}

var ws2 = Process.getModuleByName("ws2_32.dll");

function getExport(name) {
    try { return ws2.getExportByName(name); } catch (e) { return null; }
}

function hookSend(name) {
    var p = getExport(name);
    if (!p) {
        logFile.write("[" + ts() + "] EXPORT MISSING: " + name + "\n");
        return;
    }
    Interceptor.attach(p, {
        onEnter: function (args) {
            var sock = args[0].toInt32();
            var buf = args[1];
            var len = args[2].toInt32();
            logLine(name.toUpperCase(), "SEND", sock, len, buf);
        },
        onLeave: function (ret) {}
    });
    logFile.write("[" + ts() + "] HOOKED SEND " + name + " @ " + p + "\n");
    logFile.flush();
}

function hookRecv(name) {
    var p = getExport(name);
    if (!p) {
        logFile.write("[" + ts() + "] EXPORT MISSING: " + name + "\n");
        return;
    }
    Interceptor.attach(p, {
        onEnter: function (args) {
            var sock = args[0].toInt32();
            var buf = args[1];
            var len = args[2].toInt32();
            logLine(name.toUpperCase(), "RECV", sock, len, buf);
        },
        onLeave: function (ret) {}
    });
    logFile.write("[" + ts() + "] HOOKED RECV " + name + " @ " + p + "\n");
    logFile.flush();
}

hookSend("send");
hookSend("WSASend");
hookSend("sendto");
hookSend("WSASendTo");
hookRecv("recv");
hookRecv("WSARecv");
hookRecv("recvfrom");
hookRecv("WSARecvFrom");

logFile.write("[" + ts() + "] hook2 installed\n");
logFile.flush();
console.log("[h2hook2] installed on " + Process.id);
