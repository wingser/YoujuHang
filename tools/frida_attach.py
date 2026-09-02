# -*- coding: utf-8 -*-
"""用 frida 附加 X-Zone.exe 进程并注入 hook 脚本

用法:
  python tools/frida_attach.py [--spawn] [--list]
    --spawn  启动新进程并注入（需要先关闭正在运行的 X-Zone）
    默认     附加到已运行的 X-Zone.exe
    --list   列出 X-Zone 相关进程
"""
import os
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

OUTPUT_LOG = os.path.join(os.environ.get("TEMP", r"C:\Windows\Temp"), "h2hook.log")


def find_proc(dev, names):
    for p in dev.enumerate_processes():
        n = (p.name or "").lower()
        for k in names:
            if k in n:
                return p
    return None


def list_procs():
    import frida
    mgr = frida.get_device_manager()
    dev = mgr.get_local_device()
    for p in dev.enumerate_processes():
        n = (p.name or "").lower()
        if "zone" in n or "youju" in n or "gotvg" in n or "x-zone" in n:
            print("%6d  %s" % (p.pid, p.name))


def main():
    import frida
    script_file = "h2_hook.js"
    if "--script" in sys.argv:
        script_file = sys.argv[sys.argv.index("--script") + 1]

    spawn = "--spawn" in sys.argv
    if "--proc" in sys.argv:
        proc_keys = [sys.argv[sys.argv.index("--proc") + 1]]
    else:
        proc_keys = ["x-zone", "youju"]
    if "--out" in sys.argv:
        global OUTPUT_LOG
        OUTPUT_LOG = os.path.join(os.environ.get("TEMP", r"C:\Windows\Temp"),
                                  sys.argv[sys.argv.index("--out") + 1])
    script_src = open(os.path.join(os.path.dirname(os.path.abspath(__file__)),
                                   script_file), encoding="utf-8").read()
    script_src = script_src.replace("%OUTPUT%", OUTPUT_LOG.replace("\\", "\\\\"))

    dev = frida.get_local_device()
    target = find_proc(dev, proc_keys)
    if spawn:
        exe = r"D:\Game\游聚平台\1\bin\X-Zone.exe"
        print("spawning:", exe)
        pid = dev.spawn([exe])
        print("spawned pid", pid)
        session = dev.attach(pid)
        script = session.create_script(script_src)
        script.on("message", lambda m, d: print("msg:", m, d))
        script.load()
        dev.resume(pid)
    elif target:
        print("attaching to", target.pid, target.name)
        session = dev.attach(target.pid)
        script = session.create_script(script_src)
        script.on("message", lambda m, d: print("msg:", m, d))
        script.load()
        print("hook loaded. log ->", OUTPUT_LOG)
    else:
        print("no target process found. start it first, or use --spawn")
        sys.exit(1)

    print("press Ctrl+C to detach...")
    try:
        while True:
            time.sleep(60)
    except KeyboardInterrupt:
        pass
    script.unload()
    session.detach()


if __name__ == "__main__":
    if "--list" in sys.argv:
        list_procs()
    else:
        main()
