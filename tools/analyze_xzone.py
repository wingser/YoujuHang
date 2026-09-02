# -*- coding: utf-8 -*-
"""分析 X-Zone 相关连接在 pcap 中的流量"""
import subprocess
from collections import Counter

TSHARK = r"D:\Program Files\Wireshark\tshark.exe"
PCAP = r"d:\Git\YoujuHang\captures\session_login_00010_20260826131139.pcapng"


def run(args):
    r = subprocess.run([TSHARK] + args, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    return r.stdout, r.stderr


# 找 X-Zone 相关 IP 的流：153.99.234.163 和 153.0.226.187
for ip in ["153.99.234.163", "153.0.226.187"]:
    print(f"=== {ip} ===")
    out, err = run(["-r", PCAP, "-Y", f"ip.addr=={ip}",
                    "-T", "fields", "-e", "frame.time", "-e", "ip.src",
                    "-e", "tcp.srcport", "-e", "ip.dst", "-e", "tcp.dstport",
                    "-e", "tcp.len"])
    c = Counter()
    samples = {}
    for line in out.splitlines():
        parts = line.split("\t")
        if len(parts) == 6:
            t, s, sp, d, dp, ln = parts
            try:
                ln = int(ln)
            except ValueError:
                ln = 0
            if ln > 0:
                key = (s, sp, d, dp)
                c[key] += ln
                samples.setdefault(key, t)
    if not c:
        print("  (no data frames)")
    for key, n in c.most_common(10):
        print(f"  {key[0]}:{key[1]} -> {key[2]}:{key[3]}  {n} bytes  first@{samples.get(key,'')}")
    print()
