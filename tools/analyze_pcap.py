# -*- coding: utf-8 -*-
"""分析 pcap 中 SYN 连接目标分布 + 指定 IP:port 的流量"""
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
    if len(sys.argv) > 1:
        pcap = sys.argv[1]

    # 1. SYN 目标分布
    out, _ = run(["-r", pcap, "-Y", "tcp.flags.syn==1 && tcp.flags.ack==0",
                  "-T", "fields", "-e", "ip.dst", "-e", "tcp.dstport"])
    c = Counter()
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) == 2:
            c[(parts[0], parts[1])] += 1
    print("=== SYN target distribution (top 25) ===")
    for (ip, port), n in c.most_common(25):
        print(f"{ip}:{port}\t{n}")

    # 2. 指定连接流量统计（用户操作窗口 13:10-13:13）
    print("\n=== flows with data in 13:10-13:13 ===")
    out, _ = run(["-r", pcap, "-Y", "frame.time >= \"2026-08-26 13:10:00\" && frame.time < \"2026-08-26 13:13:30\"",
                  "-T", "fields", "-e", "ip.src", "-e", "ip.dst", "-e", "tcp.srcport",
                  "-e", "tcp.dstport", "-e", "tcp.len"])
    flow = Counter()
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) == 5:
            try:
                ln = int(parts[4])
            except ValueError:
                ln = 0
            if ln > 0:
                flow[(parts[0], parts[1], parts[2], parts[3])] += ln
    for (s, d, sp, dp), n in flow.most_common(20):
        print(f"{s}:{sp} -> {d}:{dp}\t{n} bytes")


if __name__ == "__main__":
    main()
