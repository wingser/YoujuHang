# -*- coding: utf-8 -*-
"""诊断 args 类型与 readByteArray 可用性"""
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

JS = r"""
function log(msg) { console.log(msg); }
var ws2 = Process.getModuleByName("ws2_32.dll");
var sendtoP = ws2.getExportByName("sendto");
log("typeof args in hook...");
Interceptor.attach(sendtoP, {
    onEnter: function (args) {
        log("args[1] type=" + typeof args[1] + " toString=" + args[1]);
        log("typeof args[1].readByteArray = " + typeof args[1].readByteArray);
        try {
            var b = args[1].readByteArray(16);
            log("readByteArray OK: " + (b ? b.byteLength : 0));
        } catch (e) {
            log("readByteArray FAIL: " + e);
        }
        try {
            log("typeof args[1].toInt32 = " + typeof args[1].toInt32);
        } catch (e) {}
    },
    onLeave: function () {}
});
log("sendto hooked @" + sendtoP);
"""


def main():
    import frida
    dev = frida.get_local_device()
    target = None
    for p in dev.enumerate_processes():
        n = (p.name or "").lower()
        if "x-zone" in n:
            target = p
            break
    if not target:
        print("no X-Zone")
        return
    session = dev.attach(target.pid)
    script = session.create_script(JS)
    script.on("message", lambda m, d: print("msg:", m))
    script.load()
    time.sleep(20)
    script.unload()
    session.detach()
    print("done")


if __name__ == "__main__":
    main()
