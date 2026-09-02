# -*- coding: utf-8 -*-
"""pcap 分析辅助脚本

用法:
  python tools/pcap_analyze.py conv <pcap> [topN]          # 列出 TCP/UDP 会话
  python tools/pcap_analyze.py hosts <pcap> [topN]          # 按 IP 聚合字节数
  python tools/pcap_analyze.py dns <pcap>                   # DNS 查询记录
  python tools/pcap_analyze.py http <pcap>                  # HTTP 请求行
  python tools/pcap_analyze.py grep <pcap> <pattern> [n]    # 在载荷中搜索字符串
  python tools/pcap_analyze.py payload <pcap> <ipA> [ipB]   # 提取双端 TCP 载荷到 payloads/ 目录
"""
import os
import re
import subprocess
import sys

TSHARK = r"D:\Program Files\Wireshark\tshark.exe"

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def run_tshark(args):
    out = subprocess.run([TSHARK] + args, capture_output=True)
    return out.stdout.decode("utf-8", errors="replace")


def cmd_flow(pcap, ip_a, ip_b=None, maxpkts=200):
    """逐包输出某 IP(或双端) 的 TCP 载荷，保留帧边界"""
    # 找出相关 stream
    text = run_tshark([
        "-r", pcap, "-Y", "tcp.payload",
        "-T", "fields", "-e", "frame.number", "-e", "ip.src",
        "-e", "tcp.srcport", "-e", "ip.dst", "-e", "tcp.dstport",
        "-e", "tcp.stream",
    ])
    streams = set()
    for ln in text.splitlines():
        parts = ln.split("\t")
        if len(parts) < 6:
            continue
        src, dst = parts[1], parts[3]
        if ip_a in (src, dst) and (not ip_b or ip_b in (src, dst)):
            streams.add(parts[5])
    if not streams:
        print("no stream found for", ip_a, ip_b or "")
        return
    for sid in sorted(streams, key=int):
        print("=" * 70)
        print("tcp.stream =", sid)
        out = run_tshark([
            "-r", pcap, "-Y", "tcp.stream == %s and tcp.payload" % sid,
            "-T", "fields", "-e", "frame.time_relative", "-e", "ip.src",
            "-e", "tcp.srcport", "-e", "tcp.len", "-e", "tcp.payload",
        ])
        for ln in out.splitlines():
            parts = ln.split("\t")
            if len(parts) < 5:
                continue
            raw = parts[4] or ""
            if raw:
                data = bytes.fromhex(raw)
            else:
                data = b""
            asc = "".join(chr(b) if 32 <= b < 127 else "." for b in data[:32])
            print("t=%-8s %s:%-5s len=%-4d | %s" % (
                parts[0], parts[1], parts[2], int(parts[3]), asc))
            if data:
                print("         hex: " + " ".join("%02x" % b for b in data))
        print()


def cmd_conv(pcap, top_n=30):
    top_n = int(top_n)
    text = run_tshark(["-r", pcap, "-q", "-z", "conv,tcp"])
    lines = text.splitlines()
    for ln in lines[: top_n + 8]:
        print(ln)


def cmd_hosts(pcap, top_n=20):
    text = run_tshark(["-r", pcap, "-q", "-z", "io,stat,0"])
    # 用 endpoints 更合适
    text = run_tshark(["-r", pcap, "-q", "-z", "endpoints,tcp"])
    lines = text.splitlines()
    # 找数据行（含 IP:port）
    for ln in lines:
        if re.search(r"\d+\.\d+\.\d+\.\d+:\d+", ln):
            print(ln)


def cmd_dns(pcap):
    text = run_tshark([
        "-r", pcap, "-Y", "dns.flags.response == 0 and dns.qry.name",
        "-T", "fields", "-e", "dns.qry.name", "-e", "dns.a", "-e", "dns.aaaa",
    ])
    seen = {}
    for ln in text.splitlines():
        parts = ln.split("\t")
        if len(parts) >= 1:
            name = parts[0]
            ip = parts[1] if len(parts) > 1 and parts[1] else (parts[2] if len(parts) > 2 else "")
            if name and ip and ip not in seen.get(name, []):
                seen.setdefault(name, []).append(ip)
    for name, ips in seen.items():
        print(name, "->", ",".join(ips))


def cmd_http(pcap):
    text = run_tshark([
        "-r", pcap, "-Y", "http.request or http.response",
        "-T", "fields", "-e", "frame.number", "-e", "ip.src",
        "-e", "tcp.srcport", "-e", "http.request.method",
        "-e", "http.host", "-e", "http.request.uri",
        "-e", "http.response.code",
    ])
    for ln in text.splitlines():
        print(ln)


def cmd_grep(pcap, pattern, n=20):
    try:
        re.compile(pattern)
    except re.error:
        # 非正则，按字面量处理
        pattern = re.escape(pattern)
    text = run_tshark([
        "-r", pcap, "-Y", "tcp.payload",
        "-T", "fields", "-e", "frame.number", "-e", "ip.src",
        "-e", "tcp.srcport", "-e", "ip.dst", "-e", "tcp.dstport",
        "-e", "tcp.payload",
    ])
    count = 0
    for ln in text.splitlines():
        parts = ln.split("\t")
        if len(parts) < 6:
            continue
        try:
            raw = bytes.fromhex(parts[5])
        except ValueError:
            continue
        try:
            s = raw.decode("utf-8", errors="replace")
        except Exception:
            s = ""
        if re.search(pattern, s, re.IGNORECASE):
            print("frame=%s %s:%s -> %s:%s | %r" % (
                parts[0], parts[1], parts[2], parts[3], parts[4], s[:120]))
            count += 1
            if count >= n:
                break


def cmd_payload(pcap, ip_a, ip_b=None):
    outdir = os.path.join(os.path.dirname(os.path.abspath(pcap)), "payloads")
    os.makedirs(outdir, exist_ok=True)
    fname = os.path.basename(pcap).replace(".pcapng", "")
    flows = {}
    # 提取每个方向的所有 tcp 载荷
    text = run_tshark([
        "-r", pcap, "-Y", "tcp.payload",
        "-T", "fields", "-e", "frame.number", "-e", "ip.src",
        "-e", "tcp.srcport", "-e", "ip.dst", "-e", "tcp.dstport",
        "-e", "tcp.payload",
    ])
    for ln in text.splitlines():
        parts = ln.split("\t")
        if len(parts) < 6:
            continue
        src, sport, dst, dport = parts[1], parts[2], parts[3], parts[4]
        if ip_a not in (src, dst):
            continue
        if ip_b and ip_b not in (src, dst):
            continue
        if ip_b is None:
            key = (src, sport, dst, dport)
        else:
            # 规范化：总是 ip_a 在前
            if src == ip_a:
                key = ("out", dst, dport)
            else:
                key = ("in", src, sport)
        try:
            raw = bytes.fromhex(parts[5])
        except ValueError:
            continue
        flows.setdefault(key, bytearray()).extend(raw)
    if not flows:
        print("no payload found for", ip_a, ip_b or "")
        return
    for key, data in sorted(flows.items()):
        if ip_b is None:
            name = "%s_%s-%s_%s.bin" % (key[0], key[1], key[2], key[3])
        else:
            name = "%s_%s-%s_%s.bin" % (key[0], key[1], key[2], key[3]) if key[0] == "out" \
                else "in_%s-%s.bin" % (key[1], key[2])
        path = os.path.join(outdir, name)
        with open(path, "wb") as f:
            f.write(data)
        print("%s  %d bytes" % (name, len(data)))


CMDS = {
    "conv": cmd_conv,
    "flow": cmd_flow,
    "hosts": cmd_hosts,
    "dns": cmd_dns,
    "http": cmd_http,
    "grep": cmd_grep,
    "payload": cmd_payload,
}


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(1)
    cmd = sys.argv[1]
    if cmd not in CMDS:
        print("unknown cmd:", cmd)
        sys.exit(1)
    args = sys.argv[2:]
    if cmd == "conv":
        cmd_conv(*args)
    elif cmd == "flow":
        cmd_flow(*args)
    elif cmd == "hosts":
        cmd_hosts(*args)
    elif cmd == "dns":
        cmd_dns(*args)
    elif cmd == "http":
        cmd_http(*args)
    elif cmd == "grep":
        cmd_grep(*args)
    elif cmd == "payload":
        cmd_payload(*args)
