# -*- coding: utf-8 -*-
"""等待 X-Zone.exe 进程出现后 attach 注入 h2_hook5.js，记录完整登录+业务流量
用法: python tools/run_hook_wait.py [log_name] [proc_key]
例:   python tools/run_hook_wait.py h7_xzone.log
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
LOGNAME = sys.argv[1] if len(sys.argv) > 1 else "h7_xzone.log"
KEY = sys.argv[2] if len(sys.argv) > 2 else "x-zone"
OUT_LOG = os.path.join(TEMP, LOGNAME)


def find_pid(dev, key):
    for p in dev.enumerate_processes():
        if key.lower() in (p.name or "").lower():
            return p.pid
    return None


def main():
    import frida
    dev = frida.get_local_device()
    print("等待进程: %s ... (Ctrl+C 退出)" % KEY)

    # 等待旧进程退出
    while True:
        try:
            if find_pid(dev, KEY) is None:
                break
        except Exception:
            pass
        time.sleep(1)
    print("旧进程已退出")

    # 等待新进程出现
    pid = None
    while True:
        try:
            pid = find_pid(dev, KEY)
        except Exception:
            pid = None
        if pid:
            break
        time.sleep(0.5)
    print("检测到新进程 pid=%d，等待 2 秒后注入..." % pid)
    time.sleep(2)

    src = open(os.path.join(BASE, "h2_hook5.js"), encoding="utf-8").read()
    js = src.replace("%OUTPUT%", OUT_LOG.replace("\\", "\\\\"))
    session = dev.attach(pid)
    script = session.create_script(js)
    script.on("message", lambda m, d: print("[msg]", m))
    script.load()
    print("attached pid=%d, log -> %s" % (pid, OUT_LOG))
    # 监听进程退出，自动退出
    def on_detached(reason, conn):
        print("process detached: %s" % reason)
        os._exit(0)
    session.on("detached", on_detached)
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
