# -*- coding: utf-8 -*-
"""分析 pcap 最近 N 分钟的 TCP 流分布"""
import subprocess
import sys
from collections import Counter

TSHARK = r"D:\Program Files\Wireshark\tshark.exe"


def run(args):
    r = subprocess.run([TSHARK] + args, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    return r.stdout, r.stderr


def main():
    pcap = r"d:\Git\YoujuHang\captures\test_capture.pcapng"
    # 获取最新帧时间
    out, err = run(["-r", pcap, "-T", "fields", "-e", "frame.time", "-c", "1",
                    "-o", "uat:user_dlts:{}"])
    # 用 last packet 时间
    out2, _ = run(["-r", pcap, "-T", "fields", "-e", "frame.time"])
    lines = out2.splitlines()
    if not lines:
        print("no packets")
        return
    print("first:", lines[0])
    print("last:", lines[-1])
    print("packets:", len(lines))


if __name__ == "__main__":
    main()
