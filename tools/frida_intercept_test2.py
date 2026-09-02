# -*- coding: utf-8 -*-
"""决定性实验2：hook GetTickCount / Sleep 等高频 API 验证 Interceptor 是否全局生效"""
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

JS = r"""
function log(msg) { console.log(msg); }

var hits = {};
function install(mod, name) {
    var m = Process.getModuleByName(mod);
    var p;
    try { p = m.getExportByName(name); } catch (e) { p = null; }
    if (!p) { log(mod + "!" + name + " NOT FOUND"); return; }
    hits[name] = 0;
    Interceptor.attach(p, {
        onEnter: function () { hits[name]++; },
        onLeave: function () {}
    });
    log("hooked " + mod + "!" + name + " @" + p);
}

install("kernel32.dll", "GetTickCount");
install("kernel32.dll", "Sleep");
install("kernelbase.dll", "GetTickCount64");
install("kernel32.dll", "QueryPerformanceCounter");
install("user32.dll", "GetMessageW");

// 自调用验证（当前线程）
var gt = new NativeFunction(Process.getModuleByName("kernel32.dll").getExportByName("GetTickCount"), "uint", []);
for (var i = 0; i < 3; i++) gt();

setTimeout(function () {
    log("=== stats after 15s ===");
    for (var k in hits) log("  " + k + " = " + hits[k]);
    log("=== done ===");
}, 15000);
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
    print("attach", target.pid)
    session = dev.attach(target.pid)
    script = session.create_script(JS)
    script.on("message", lambda m, d: print("msg:", m))
    script.load()
    time.sleep(18)
    script.unload()
    session.detach()
    print("done")


if __name__ == "__main__":
    main()
