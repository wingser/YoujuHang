# -*- coding: utf-8 -*-
"""诊断 frida 17 ia32 可用的内存读取 API"""
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

JS = r"""
function log(msg) { console.log(msg); }
log("frida ver: " + (typeof Frida !== "undefined" ? Frida.version : "?"));
var tests = {
    "Memory.readByteArray": function () { return typeof Memory.readByteArray; },
    "Memory.readU8": function () { return typeof Memory.readU8; },
    "Memory.readU16": function () { return typeof Memory.readU16; },
    "Memory.readU32": function () { return typeof Memory.readU32; },
    "Memory.readAnsiString": function () { return typeof Memory.readAnsiString; },
    "Memory.readUtf8String": function () { return typeof Memory.readUtf8String; },
    "Memory.readCString": function () { return typeof Memory.readCString; },
    "Memory.copy": function () { return typeof Memory.copy; },
    "Memory.protect": function () { return typeof Memory.protect; },
    "hexdump": function () { return typeof hexdump; },
    "ptr.readByteArray": function () { return typeof NativePointer.prototype.readByteArray; },
    "ptr.readU8": function () { return typeof NativePointer.prototype.readU8; },
    "ptr.readAnsiString": function () { return typeof NativePointer.prototype.readAnsiString; },
};
for (var k in tests) {
    try { log(k + " = " + tests[k]()); } catch (e) { log(k + " THREW " + e); }
}

// 实际尝试
var buf = Memory.alloc(16);
try {
    var arr = new Uint8Array(Memory.readByteArray(buf, 16));
    log("readByteArray OK: " + arr.length);
} catch (e) { log("readByteArray FAIL: " + e); }
try {
    log("hexdump: " + hexdump(buf, { length: 16 }));
} catch (e) { log("hexdump FAIL: " + e); }
try {
    var a = Memory.readAnsiString(buf, 16);
    log("readAnsiString OK: len=" + (a ? a.length : 0));
} catch (e) { log("readAnsiString FAIL: " + e); }
try {
    var b = buf.readByteArray(16);
    log("ptr.readByteArray OK: " + (b ? b.byteLength : 0));
} catch (e) { log("ptr.readByteArray FAIL: " + e); }
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
    time.sleep(2)
    script.unload()
    session.detach()
    print("done")


if __name__ == "__main__":
    main()
