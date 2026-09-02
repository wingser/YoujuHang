# -*- coding: utf-8 -*-
"""决定性测试：self-call send 能否触发 Interceptor hook 的 onEnter"""
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

JS = r"""
function log(msg) { console.log(msg); }

var ws2 = Process.getModuleByName("ws2_32.dll");
var sendP = ws2.getExportByName("send");
log("send@" + sendP);

var hits = 0;
try {
    Interceptor.attach(sendP, {
        onEnter: function (args) {
            hits++;
            log("SEND onEnter hit#" + hits + " sock=" + args[0].toInt32() + " len=" + args[2].toInt32());
        },
        onLeave: function (ret) {
            log("SEND onLeave ret=" + ret);
        }
    });
    log("send hook attached OK");
} catch (e) {
    log("attach THREW: " + e);
}

// self-call 5 次
try {
    var fn = new NativeFunction(sendP, "int", ["int", "pointer", "int", "int"]);
    var buf = Memory.alloc(16);
    for (var i = 0; i < 5; i++) {
        var r = fn(12345, buf, 8, 0);
        log("self-call #" + i + " ret=" + r);
    }
} catch (e) {
    log("self-call THREW: " + e);
}

// 等待，看是否收到其他线程的 send（客户端心跳）
log("now waiting for client traffic 15s...");
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
    time.sleep(15)
    script.unload()
    session.detach()
    print("done")


if __name__ == "__main__":
    main()
