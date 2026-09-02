# -*- coding: utf-8 -*-
"""frida 探针：验证 send hook 是否生效
1. attach X-Zone.exe
2. hook ws2_32 send（记录）
3. 进程内主动调用 send(sock=999, buf, len) 验证 hook 触发
"""
import os
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

OUT = os.path.join(os.environ.get("TEMP", r"C:\Windows\Temp"), "h2probe.log")

JS = r"""
var outPath = "%OUTPUT%";
var logFile = new File(outPath, "w");
function ts() { return Date.now() % 100000000; }

var ws2 = Process.getModuleByName("ws2_32.dll");
var sendP = ws2.getExportByName("send");
var hits = 0;

Interceptor.attach(sendP, {
    onEnter: function (args) {
        hits++;
        logFile.write("[" + ts() + "] SEND hook hit #" + hits +
            " sock=" + args[0].toInt32() + " len=" + args[2].toInt32() +
            " retAddr=" + this.returnAddress + "\n");
        logFile.flush();
    },
    onLeave: function (ret) {}
});
logFile.write("[" + ts() + "] probe installed, send@0x" + sendP.toString(16) + "\n");
logFile.flush();

// 主动调用 send（无效 socket，仅验证 onEnter 触发）
setTimeout(function () {
    try {
        var fn = new NativeFunction(sendP, "int", ["int", "pointer", "int", "int"]);
        var buf = Memory.alloc(4);
        Memory.writeU32(buf, 0x41424344);
        var r = fn(999, buf, 4, 0);
        logFile.write("[" + ts() + "] self-call send ret=" + r + "\n");
    } catch (e) {
        logFile.write("[" + ts() + "] self-call error: " + e + "\n");
    }
    logFile.flush();
}, 2000);

console.log("probe installed on " + Process.id);
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
    src = JS.replace("%OUTPUT%", OUT.replace("\\", "\\\\"))
    script = session.create_script(src)
    script.on("message", lambda m, d: print("msg:", m))
    script.load()
    print("waiting 5s...")
    time.sleep(5)
    print("done")


if __name__ == "__main__":
    main()
