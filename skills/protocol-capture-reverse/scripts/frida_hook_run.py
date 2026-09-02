# -*- coding: utf-8 -*-
"""把 frida_socket_hook.js 注入目标进程，抓取 send/recv 明文。

适用场景：流量被 TLS/自定义加密包裹，Wireshark 只能看到密文。
在 send/recv 层 hook 可拿到加解密前后的明文，与 pcap 互为补充。

前置:
    pip install frida frida-tools
    本机需已运行 frida-server（Android）或目标为 Windows 本机进程（直接 attach）

用法:
    python frida_hook_run.py <进程名或PID> <输出日志> [--spawn]
    python frida_hook_run.py X-Zone.exe D:/tmp/hook.log
    python frida_hook_run.py 12345 D:/tmp/hook.log

不带 --spawn 时直接 attach 到已有进程（推荐：可等客户端登录后再挂，避免错过初始化）。
带 --spawn 时由 frida 启动进程（适合必须抓取启动阶段流量的场景）。

运行后按 Ctrl+C 结束并 detach。
"""
import os
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

HERE = os.path.dirname(os.path.abspath(__file__))
JS = os.path.join(HERE, "frida_socket_hook.js")


def main():
    argv = [a for a in sys.argv[1:] if not a.startswith("--")]
    flags = set(a for a in sys.argv[1:] if a.startswith("--"))
    if len(argv) < 2:
        print(__doc__)
        return 1
    target, out_path = argv[0], argv[1]

    try:
        import frida
    except ImportError:
        print("缺少 frida 包，请先: pip install frida frida-tools")
        return 1

    js = open(JS, encoding="utf-8").read().replace("%OUTPUT%", out_path.replace("\\", "/"))

    try:
        if "--spawn" in flags:
            pid = frida.spawn(target.split(os.sep)[-1])
            session = frida.attach(pid)
            frida.resume(pid)
            print("spawned pid=%d" % pid)
        else:
            try:
                pid = int(target)
                session = frida.attach(pid)
            except ValueError:
                session = frida.attach(target)
    except Exception as e:
        print("attach 失败: %s" % e)
        print("提示: 进程名要带 .exe；也可改用 PID。用 tasklist 确认进程存在。")
        return 1

    def on_msg(message, data):
        if message.get("type") == "error":
            print("[js error] %s" % message.get("stack", message))
        else:
            print("[js] %s" % message.get("payload"))

    script = session.create_script(js)
    script.on("message", on_msg)
    script.load()
    print("hook 已注入，日志 -> %s" % out_path)
    print("操作客户端触发目标流量；按 Ctrl+C 结束。")
    try:
        while True:
            time.sleep(1)
    except KeyboardInterrupt:
        print("\n结束")
    finally:
        try:
            session.detach()
        except Exception:
            pass
    return 0


if __name__ == "__main__":
    sys.exit(main())
