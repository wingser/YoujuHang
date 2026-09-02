# -*- coding: utf-8 -*-
"""多进程 frida hook：同时附加 X-Zone / gotvg_web / XZDesktop64，各自独立日志
用法: python tools/frida_multi.py [--script h2_hook4.js]
"""
import os
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

TEMP = os.environ.get("TEMP", r"C:\Windows\Temp")
SCRIPT = "h2_hook4.js"
if "--script" in sys.argv:
    SCRIPT = sys.argv[sys.argv.index("--script") + 1]

TARGETS = [
    ("x-zone", "h4_xzone.log"),
    ("gotvg_web", "h4_gotvg.log"),
    ("xzdesktop", "h4_xzdesktop.log"),
]

sessions = []
scripts = []


def main():
    import frida
    dev = frida.get_local_device()
    src = open(os.path.join(os.path.dirname(os.path.abspath(__file__)), SCRIPT),
               encoding="utf-8").read()

    for key, logname in TARGETS:
        target = None
        for p in dev.enumerate_processes():
            n = (p.name or "").lower()
            if key in n:
                target = p
                break
        if not target:
            print(f"[{key}] not running, skip")
            continue
        out_log = os.path.join(TEMP, logname)
        js = src.replace("%OUTPUT%", out_log.replace("\\", "\\\\"))
        session = dev.attach(target.pid)
        script = session.create_script(js)
        script.on("message", lambda m, d, k=key: print(f"[{k}] msg:", m))
        script.load()
        sessions.append(session)
        scripts.append(script)
        print(f"[{key}] attached {target.pid}, log -> {out_log}")

    print("all attached, keeping alive (Ctrl+C to detach)...")
    try:
        while True:
            time.sleep(60)
    except KeyboardInterrupt:
        pass
    for s in scripts:
        try:
            s.unload()
        except Exception:
            pass
    for s in sessions:
        try:
            s.detach()
        except Exception:
            pass
    print("done")


if __name__ == "__main__":
    main()
