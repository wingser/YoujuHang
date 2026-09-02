# -*- coding: utf-8 -*-
"""最小化 frida 测试：验证 API 可用性"""
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

JS = r"""
var p = Module.findExportByName("ws2_32.dll", "send");
console.log("send addr: " + p);
if (p) {
    Interceptor.attach(p, {
        onEnter: function (args) {
            this.n = args[2].toInt32();
        },
        onLeave: function (ret) {
            console.log("send ret " + ret);
        }
    });
}
var p2 = Module.findExportByName("ws2_32.dll", "connect");
console.log("connect addr: " + p2);
if (p2) {
    Interceptor.attach(p2, {
        onEnter: function (args) { console.log("connect called"); }
    });
}
console.log("OK");
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
    print("loaded, waiting 3s...")
    import time
    time.sleep(3)
    script.unload()
    session.detach()
    print("done")


if __name__ == "__main__":
    main()
