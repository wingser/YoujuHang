# -*- coding: utf-8 -*-
"""诊断 frida 在 32 位 X-Zone 进程中的 API 可用性"""
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

JS = r"""
function log(msg) { console.log(msg); }

var checks = [
    ["Process.arch", function () { return Process.arch; }],
    ["Module.findExportByName", function () { return typeof Module.findExportByName; }],
    ["NativeFunction", function () { return typeof NativeFunction; }],
    ["Interceptor", function () { return typeof Interceptor; }],
    ["Interceptor.attach", function () { return typeof Interceptor.attach; }],
    ["Memory", function () { return typeof Memory; }],
    ["Memory.alloc", function () { return typeof Memory.alloc; }],
    ["Memory.readU32", function () { return typeof Memory.readU32; }],
    ["Memory.writeU32", function () { return typeof Memory.writeU32; }],
    ["Thread.backtrace", function () { return typeof Thread.backtrace; }],
    ["Backtracer", function () { return typeof Backtracer; }],
    ["File", function () { return typeof File; }],
    ["NativePointer", function () { return typeof NativePointer; }],
    ["Process.getModuleByName", function () { return typeof Process.getModuleByName; }],
];
for (var i = 0; i < checks.length; i++) {
    try {
        log(checks[i][0] + " = " + checks[i][1]());
    } catch (e) {
        log(checks[i][0] + " THREW: " + e);
    }
}

// 实际操作测试
try {
    var m = Memory.alloc(8);
    Memory.writeU32(m, 0x12345678);
    var v = Memory.readU32(m);
    log("alloc/write/read U32 OK v=" + v.toString(16));
} catch (e) {
    log("Memory ops THREW: " + e);
}

try {
    var ws2 = Process.getModuleByName("ws2_32.dll");
    var sendP = ws2.getExportByName("send");
    log("send export = " + sendP);
    var fn = new NativeFunction(sendP, "int", ["int", "pointer", "int", "int"]);
    log("NativeFunction created OK");
    var buf = Memory.alloc(4);
    var r = fn(999, buf, 4, 0);
    log("self send ret=" + r);
} catch (e) {
    log("NativeFunction path THREW: " + e);
}

// attach 一个临时 hook 验证 Interceptor 是否真拦截
try {
    var ws2 = Process.getModuleByName("ws2_32.dll");
    var recvP = ws2.getExportByName("recv");
    var hits = 0;
    Interceptor.attach(recvP, {
        onEnter: function (args) {
            hits++;
            log("RECV hit #" + hits + " sock=" + args[0].toInt32());
        },
        onLeave: function (r) {}
    });
    log("recv hook attached, waiting for activity...");
} catch (e) {
    log("Interceptor path THREW: " + e);
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
    print("waiting 20s (to catch recv activity)...")
    time.sleep(20)
    script.unload()
    session.detach()
    print("done")


if __name__ == "__main__":
    main()
