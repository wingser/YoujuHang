# -*- coding: utf-8 -*-
"""tshark 抓包控制脚本（避开 shell 引号问题）

用法:
  python tools/capture_ctrl.py start <out.pcapng> [iface_index]
      iface_index 默认 5 (WLAN 4)，可用 list 查看
  python tools/capture_ctrl.py stop
  python tools/capture_ctrl.py list
  python tools/capture_ctrl.py status
"""
import configparser
import os
import subprocess
import sys
import time

# 优先从 tool_paths.ini 读取（避免路径写死找不到），缺失时回退到硬编码
_INI = os.path.join(os.path.dirname(os.path.abspath(__file__)), "tool_paths.ini")
_default_tshark = r"D:\Program Files\Wireshark\tshark.exe"


def _load_tshark_path():
    try:
        cp = configparser.ConfigParser()
        cp.read(_INI)
        return cp.get("Wireshark", "tshark", fallback=_default_tshark)
    except Exception:
        return _default_tshark


TSHARK = _load_tshark_path()
PID_FILE = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".tshark.pid")


def list_interfaces():
    out = subprocess.run([TSHARK, "-D"], capture_output=True)
    text = out.stdout.decode("utf-8", errors="replace")
    if not text:
        text = out.stderr.decode("utf-8", errors="replace")
    print(text)


def start(out_path, iface="5", extra_filter=None, rotate_mb=0):
    """启动后台抓包。rotate_mb>0 时按大小轮换文件（文件名自动加序号）。"""
    if os.path.exists(PID_FILE):
        old = open(PID_FILE).read().strip()
        print("WARN: tshark may already be running (pid=%s). Stop first." % old)
        return 1
    os.makedirs(os.path.dirname(os.path.abspath(out_path)), exist_ok=True)
    # 后台启动，stdout/stderr 重定向到日志文件
    log_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".tshark.log")
    flog = open(log_path, "w")
    args = [TSHARK, "-i", iface]
    if extra_filter:
        args += ["-f", extra_filter]
    if rotate_mb > 0:
        args += ["-b", "filesize:%d" % (rotate_mb * 1024)]
    args += ["-w", os.path.abspath(out_path)]
    proc = subprocess.Popen(args, stdout=flog, stderr=subprocess.STDOUT)
    with open(PID_FILE, "w") as f:
        f.write(str(proc.pid))
    time.sleep(2)
    if proc.poll() is not None:
        print("FAILED: tshark exited early. Log:")
        print(open(log_path).read())
        return 1
    print("OK capture started pid=%s -> %s (rotate=%dMB, filter=%s)"
          % (proc.pid, out_path, rotate_mb, extra_filter or "ALL"))
    return 0


def stop():
    if not os.path.exists(PID_FILE):
        print("No capture running.")
        return
    pid = int(open(PID_FILE).read().strip())
    try:
        # 先优雅结束 tshark（会 flush pcap 头），再兜底 kill
        subprocess.run(["taskkill", "/PID", str(pid), "/T"], capture_output=True)
        time.sleep(1)
        if os.path.exists(PID_FILE):
            os.remove(PID_FILE)
        print("OK capture stopped (pid=%s)" % pid)
    except Exception as e:
        print("ERR", e)
    log_path = os.path.join(os.path.dirname(os.path.abspath(__file__)), ".tshark.log")
    if os.path.exists(log_path):
        print("--- tshark log tail ---")
        print(open(log_path).read()[-2000:])


def status():
    if not os.path.exists(PID_FILE):
        print("not running")
        return
    pid = int(open(PID_FILE).read().strip())
    print("running pid=%s" % pid)


if __name__ == "__main__":
    cmd = sys.argv[1] if len(sys.argv) > 1 else "list"
    if cmd == "start":
        if len(sys.argv) < 3:
            print("usage: capture_ctrl.py start <out.pcapng> [iface] [filter] [rotate_mb]")
            sys.exit(1)
        out_path = sys.argv[2]
        iface = sys.argv[3] if len(sys.argv) > 3 else "5"
        filt = sys.argv[4] if len(sys.argv) > 4 else None
        rotate = int(sys.argv[5]) if len(sys.argv) > 5 else 0
        sys.exit(start(out_path, iface, filt, rotate))
    elif cmd == "stop":
        stop()
    elif cmd == "list":
        list_interfaces()
    elif cmd == "status":
        status()
    else:
        print("unknown cmd:", cmd)
