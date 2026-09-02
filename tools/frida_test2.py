# -*- coding: utf-8 -*-
"""frida 17 API 验证"""
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

JS = r"""
console.log("Process.arch=" + Process.arch + " pointerSize=" + Process.pointerSize);
var ws2 = Process.getModuleByName("ws2_32.dll");
console.log("ws2 base=" + ws2.base);
try {
    var p = ws2.getExportByName("send");
    console.log("send export=" + p);
    if (p) {
        Interceptor.attach(p, {
            onEnter: function (args) {
                console.log("  SEND sock=" + args[0] + " len=" + args[2]);
            },
            onLeave: function (ret) {
                console.log("  SEND ret=" + ret);
            }
        });
        console.log("send hook OK");
    }
} catch (e) {
    console.log("send hook FAIL: " + e);
}
try {
    var c = ws2.getExportByName("connect");
    console.log("connect export=" + c);
    if (c) Interceptor.attach(c, { onEnter: function (a) { console.log("  CONNECT"); } });
    console.log("connect hook OK");
} catch (e) {
    console.log("connect hook FAIL: " + e);
}
console.log("TEST DONE");
"""

import frida


def main():
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
    print("attach", target.pid)
    session = dev.attach(target.pid)
    script = session.create_script(JS)
    script.on("message", lambda m, d: print("msg:", m))
    script.load()
    print("loaded, waiting 5s...")
    time.sleep(5)
    script.unload()
    session.detach()
    print("done")


if __name__ == "__main__":
    main()
