# -*- coding: utf-8 -*-
"""测试 mswsock.dll 的 WSP 导出是否存在，并 self-call 验证 hook"""
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

JS = r"""
function log(msg) { console.log(msg); }
var ms = Process.getModuleByName("mswsock.dll");
log("mswsock base=" + ms.base + " size=" + ms.size);
var exports = ms.enumerateExports();
var names = [];
for (var i = 0; i < exports.length; i++) {
    if (exports[i].name.indexOf("WSP") === 0) {
        names.push(exports[i].name + "@" + exports[i].address);
    }
}
log("WSP exports (" + names.length + "):");
for (var j = 0; j < names.length; j++) log("  " + names[j]);
"""


def main():
    import frida
    dev = frida.get_local_device()
    for key in ["x-zone"]:
        target = None
        for p in dev.enumerate_processes():
            n = (p.name or "").lower()
            if key in n:
                target = p
                break
        if target:
            break
    if not target:
        print("no target")
        return
    print("attach", target.pid)
    session = dev.attach(target.pid)
    script = session.create_script(JS)
    script.on("message", lambda m, d: print("msg:", m))
    script.load()
    time.sleep(2)
    script.unload()
    session.detach()
    print("done")


if __name__ == "__main__":
    main()
