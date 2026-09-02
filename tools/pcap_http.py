# -*- coding: utf-8 -*-
"""解析 pcapng，提取指定端口的 TCP 流并重组 HTTP 请求/响应"""
import sys
import dpkt
import io

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

def main():
    pcap = sys.argv[1]
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 18000
    direction = sys.argv[3] if len(sys.argv) > 3 else "all"  # all/client/server
    f = open(pcap, "rb")
    try:
        reader = dpkt.pcapng.Reader(f)
    except Exception:
        f.close()
        f = open(pcap, "rb")
        reader = dpkt.pcap.Reader(f)
    streams = {}
    for ts, buf in reader:
        try:
            eth = dpkt.ethernet.Ethernet(buf)
        except Exception:
            continue
        if eth.type != 0x0800 and eth.type != 0x86dd:
            continue
        ip = eth.data
        if isinstance(ip, dpkt.ip6.IP6):
            src = ip.src
            dst = ip.dst
        elif isinstance(ip, dpkt.ip.IP):
            src = ip.src
            dst = ip.dst
        else:
            continue
        if not isinstance(ip.data, dpkt.tcp.TCP):
            continue
        tcp = ip.data
        sport = tcp.sport
        dport = tcp.dport
        if sport != port and dport != port:
            continue
        key = (ip.data, sport, dport)
        streams.setdefault((str(src), str(dst), sport, dport), []).append((ts, tcp.seq, tcp.flags, bytes(tcp.data)))
    # 重组每个流
    for key, pkts in streams.items():
        src, dst, sport, dport = key
        pkts.sort(key=lambda x: x[1])
        data = b""
        last_seq = None
        for ts, seq, flags, d in pkts:
            if last_seq is not None and seq != last_seq + len(data):
                pass
            data += d
        print("=" * 70)
        print("stream %s:%d -> %s:%d total=%d bytes" % (src, sport, dst, dport, len(data)))
        # 分割 HTTP 请求/响应
        print("--- content (first 3000 bytes) ---")
        try:
            print(data[:3000].decode("utf-8", "replace"))
        except Exception:
            print(repr(data[:3000]))

if __name__ == "__main__":
    main()
