# -*- coding: utf-8 -*-
"""检查 X-Zone.exe 加载的网络相关模块路径"""
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

JS = r"""
function log(msg) { console.log(msg); }
var mods = Process.enumerateModules();
var interesting = [];
for (var i = 0; i < mods.length; i++) {
    var m = mods[i];
    var n = m.name.toLowerCase();
    if (n.indexOf("ws2") >= 0 || n.indexOf("mswsock") >= 0 || n.indexOf("wininet") >= 0 ||
        n.indexOf("winhttp") >= 0 || n.indexOf("sspicli") >= 0 || n.indexOf("schannel") >= 0 ||
        n.indexOf("wsock") >= 0 || n.indexOf("secur32") >= 0 || n.indexOf("iphlpapi") >= 0 ||
        n.indexOf("dnsapi") >= 0 || n.indexOf("netio") >= 0) {
        interesting.push(m.name + "  " + m.path);
    }
}
log("total modules: " + mods.length);
for (var j = 0; j < interesting.length; j++) {
    log("NET: " + interesting[j]);
}
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
    time.sleep(2)
    script.unload()
    session.detach()
    print("done")


if __name__ == "__main__":
    main()
