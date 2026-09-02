# -*- coding: utf-8 -*-
"""注入 h2_hook5.js 到 X-Zone.exe，日志 -> %TEMP%\\h5_xzone.log
用法: python tools/run_hook_h5.py [pid]
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
OUT_LOG = os.path.join(TEMP, "h5_xzone.log")


def main():
    import frida
    pid = int(sys.argv[1]) if len(sys.argv) > 1 else None
    dev = frida.get_local_device()
    if pid is None:
        for p in dev.enumerate_processes():
            if (p.name or "").lower().startswith("x-zone"):
                pid = p.pid
                break
    if not pid:
        print("X-Zone.exe not found")
        sys.exit(1)
    src = open(os.path.join(BASE, "h2_hook5.js"), encoding="utf-8").read()
    js = src.replace("%OUTPUT%", OUT_LOG.replace("\\", "\\\\"))
    session = dev.attach(pid)
    script = session.create_script(js)
    script.on("message", lambda m, d: print("[msg]", m))
    script.load()
    print("attached pid=%d, log -> %s" % (pid, OUT_LOG))
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
