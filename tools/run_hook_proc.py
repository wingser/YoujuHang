# -*- coding: utf-8 -*-
"""注入 h2_hook5.js 到指定进程，独立日志
用法: python tools/run_hook_proc.py <进程名关键字> <日志名> [script]
例:   python tools/run_hook_proc.py gotvg_web h5_gotvg.log
"""
import os
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

TEMP = os.environ.get("TEMP", r"C:\Windows\Temp")
BASE = os.path.dirname(os.path.abspath(__file__))
KEY = sys.argv[1]
LOGNAME = sys.argv[2]
SCRIPT = "h2_hook5.js"
if len(sys.argv) > 3:
    SCRIPT = sys.argv[3]
OUT_LOG = os.path.join(TEMP, LOGNAME)


def main():
    import frida
    dev = frida.get_local_device()
    pid = None
    for p in dev.enumerate_processes():
        if KEY.lower() in (p.name or "").lower():
            pid = p.pid
            break
    if not pid:
        print("%s not found" % KEY)
        sys.exit(1)
    src = open(os.path.join(BASE, SCRIPT), encoding="utf-8").read()
    js = src.replace("%OUTPUT%", OUT_LOG.replace("\\", "\\\\"))
    session = dev.attach(pid)
    script = session.create_script(js)
    script.on("message", lambda m, d: print("[msg]", m))
    script.load()
    print("attached %s pid=%d, log -> %s" % (KEY, pid, OUT_LOG))
    try:
        while True:
            time.sleep(60)
    except KeyboardInterrupt:
        pass
    try:
        script.unload()
    except Exception:
        pass
    try:
        session.detach()
    except Exception:
        pass
    print("done")


if __name__ == "__main__":
    main()
