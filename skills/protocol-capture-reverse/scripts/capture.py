# -*- coding: utf-8 -*-
"""tshark 后台抓包控制（Windows）。

封装 tshark 的常驻抓包：后台启停、PID 管理、按大小轮转、BPF 过滤。
用 Python 起进程而不是手写命令行，是为了避开 Windows shell 的引号转义问题。

用法:
    python capture.py list                                 列出网卡
    python capture.py start <out.pcapng> [iface] [filter] [rotate_mb]
    python capture.py status
    python capture.py stop

示例:
    python capture.py list
    python capture.py start D:/Git/YoujuHang/captures/join_20260830.pcapng 5 "tcp or port 53" 50
    python capture.py stop

tshark 路径按以下顺序查找：
  1. 环境变量 TSHARK
  2. 项目 tools/tool_paths.ini 的 [Wireshark] tshark
  3. 常见安装目录
  4. PATH
"""
import configparser
import os
import subprocess
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

_HERE = os.path.dirname(os.path.abspath(__file__))
PID_FILE = os.path.join(_HERE, ".tshark.pid")
LOG_FILE = os.path.join(_HERE, ".tshark.log")

_FALLBACKS = [
    r"D:\Program Files\Wireshark\tshark.exe",
    r"C:\Program Files\Wireshark\tshark.exe",
]


def find_tshark():
    env = os.environ.get("TSHARK")
    if env and os.path.exists(env):
        return env
    # 向上查找项目里的 tools/tool_paths.ini
    d = _HERE
    for _ in range(6):
        ini = os.path.join(d, "tools", "tool_paths.ini")
        if os.path.exists(ini):
            try:
                cp = configparser.ConfigParser()
                cp.read(ini, encoding="utf-8")
                p = cp.get("Wireshark", "tshark", fallback="")
                if p and os.path.exists(p):
                    return p
            except Exception:
                pass
        parent = os.path.dirname(d)
        if parent == d:
            break
        d = parent
    for p in _FALLBACKS:
        if os.path.exists(p):
            return p
    return "tshark"  # 交给 PATH


TSHARK = find_tshark()


def list_interfaces():
    print("tshark: %s\n" % TSHARK)
    r = subprocess.run([TSHARK, "-D"], capture_output=True)
    text = (r.stdout or b"").decode("utf-8", "replace")
    if not text:
        text = (r.stderr or b"").decode("utf-8", "replace")
    print(text)


def start(out_path, iface="5", bpf=None, rotate_mb=0):
    if os.path.exists(PID_FILE):
        print("WARN: 可能已在抓包 (pid=%s)，先 stop" % open(PID_FILE).read().strip())
        return 1
    out_path = os.path.abspath(out_path)
    os.makedirs(os.path.dirname(out_path), exist_ok=True)
    flog = open(LOG_FILE, "w", encoding="utf-8", errors="replace")
    args = [TSHARK, "-i", str(iface)]
    if bpf:
        args += ["-f", bpf]
    if rotate_mb and rotate_mb > 0:
        args += ["-b", "filesize:%d" % (int(rotate_mb) * 1024)]
    args += ["-w", out_path]
    proc = subprocess.Popen(args, stdout=flog, stderr=subprocess.STDOUT)
    open(PID_FILE, "w").write(str(proc.pid))
    time.sleep(2)
    if proc.poll() is not None:
        print("FAILED: tshark 提前退出。日志尾部:")
        print(open(LOG_FILE, encoding="utf-8", errors="replace").read()[-2000:])
        os.remove(PID_FILE)
        return 1
    print("OK 抓包已启动 pid=%s\n   -> %s\n   iface=%s rotate=%sMB filter=%s"
          % (proc.pid, out_path, iface, rotate_mb or "-", bpf or "ALL"))
    return 0


def stop():
    if not os.path.exists(PID_FILE):
        print("当前没有在抓包")
        return 0
    pid = open(PID_FILE).read().strip()
    # taskkill /T 让 tshark 优雅退出并 flush pcap 头，直接 kill 会损坏文件
    subprocess.run(["taskkill", "/PID", pid, "/T"], capture_output=True)
    time.sleep(1)
    if os.path.exists(PID_FILE):
        os.remove(PID_FILE)
    print("OK 已停止 (pid=%s)" % pid)
    if os.path.exists(LOG_FILE):
        tail = open(LOG_FILE, encoding="utf-8", errors="replace").read()[-800:]
        if tail.strip():
            print("--- tshark 日志尾部 ---\n" + tail)
    return 0


def status():
    if not os.path.exists(PID_FILE):
        print("not running")
        return 0
    print("running pid=%s" % open(PID_FILE).read().strip())
    return 0


def _main():
    cmd = sys.argv[1] if len(sys.argv) > 1 else "list"
    if cmd == "list":
        list_interfaces()
    elif cmd == "start":
        if len(sys.argv) < 3:
            print("usage: capture.py start <out.pcapng> [iface] [filter] [rotate_mb]")
            return 1
        out = sys.argv[2]
        iface = sys.argv[3] if len(sys.argv) > 3 else "5"
        bpf = sys.argv[4] if len(sys.argv) > 4 else None
        rot = int(sys.argv[5]) if len(sys.argv) > 5 else 0
        return start(out, iface, bpf, rot)
    elif cmd == "stop":
        return stop()
    elif cmd == "status":
        return status()
    else:
        print("unknown cmd:", cmd)
        return 1
    return 0


if __name__ == "__main__":
    sys.exit(_main())
