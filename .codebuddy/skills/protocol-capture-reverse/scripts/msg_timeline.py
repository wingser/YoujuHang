# -*- coding: utf-8 -*-
"""提取 pcap 中某个 TCP 端口的消息时间线（非 HTTP/2 的自定义二进制协议用）。

适用于游聚 18140（大厅长连接）、18180（大厅推送）这类自定义封帧：
    [4B len LE][2B msgID LE][payload]      # 18140
    [00 00 XX YY][TT 00 00 07 CC][data]    # 18141 二进制帧

用法:
    python msg_timeline.py <pcap> <port>              列出该端口每条报文
    python msg_timeline.py <pcap> <port> --pb         尝试 protobuf 解码
    python msg_timeline.py <pcap> <port> --len4       按 [4B len LE][2B msgID] 解封帧
    python msg_timeline.py <pcap> <port> --client 192.168.1.9   标记方向
    python msg_timeline.py <pcap> <port> --limit 200  限制输出条数

依赖: tshark（见 capture.py 的查找逻辑）
"""
import os
import struct
import subprocess
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
try:
    from pb_decode import decode, dumps
except ImportError:
    decode = None

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

try:
    from capture import find_tshark
    TSHARK = find_tshark()
except Exception:
    TSHARK = r"D:\Program Files\Wireshark\tshark.exe"


def run_tshark(pcap, port):
    cmd = [TSHARK, "-r", pcap,
           "-Y", "tcp.port==%s && tcp.len>0" % port,
           "-T", "fields",
           "-e", "frame.number", "-e", "frame.time_relative",
           "-e", "ip.src", "-e", "tcp.srcport",
           "-e", "ip.dst", "-e", "tcp.dstport", "-e", "tcp.payload"]
    r = subprocess.run(cmd, capture_output=True)
    return r.stdout.decode("utf-8", "replace")


def fmt_proto(data, indent=5, maxlines=12):
    if decode is None or not data:
        return []
    pad = " " * indent
    try:
        lines = dumps(decode(data, maxdepth=6), show_hex=40)
    except Exception as e:
        return ["%s<decode failed: %s>" % (pad, e)]
    out = [pad + l for l in lines[:maxlines]]
    if len(lines) > maxlines:
        out.append("%s... (%d 行)" % (pad, len(lines)))
    return out


def main(pcap, port, show_pb=False, len4=False, client_ip=None, limit=0):
    text = run_tshark(pcap, port)
    n = 0
    for ln in text.splitlines():
        p = ln.strip().split("\t")
        if len(p) < 7 or not p[6]:
            continue
        fno, t, src, sp, dst, dp, hexd = p
        raw = bytes.fromhex(hexd)
        if len(raw) < 4:
            continue
        if client_ip:
            d = "C->S" if src == client_ip else "S->C"
        else:
            d = "  ? "

        msgid = None
        body = raw
        if len4 and len(raw) >= 6:
            (length,) = struct.unpack_from("<I", raw, 0)
            msgid = struct.unpack_from("<H", raw, 4)[0]
            body = raw[6:]
            head = "#%-6s t=%-11s %s len=%-5d hdrlen=%-4d msgid=%-5d" % (
                fno, t, d, len(raw), length, msgid)
        else:
            head = "#%-6s t=%-11s %s len=%-5d head=%s" % (
                fno, t, d, len(raw), raw[:6].hex())

        print(head)
        if show_pb:
            # 先按原样试解，失败再跳过 2/4 字节试（封帧长度不定，启发式）
            for skip in (0, 2, 4, 5):
                if skip >= len(body):
                    break
                lines = fmt_proto(body[skip:])
                if lines and "decode failed" not in lines[0]:
                    for l in lines:
                        print(l)
                    break
        n += 1
        if limit and n >= limit:
            print("... (达到 --limit %d)" % limit)
            break
    if n == 0:
        print("端口 %s 上没有带载荷的 TCP 报文，检查 pcap 路径/端口" % port)


def _main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    flags = set(a for a in sys.argv[1:] if a.startswith("--"))
    if len(args) < 2:
        print(__doc__)
        return 1
    client, limit = None, 0
    if "--client" in flags:
        i = sys.argv.index("--client")
        client = sys.argv[i + 1] if i + 1 < len(sys.argv) else None
    if "--limit" in flags:
        i = sys.argv.index("--limit")
        limit = int(sys.argv[i + 1]) if i + 1 < len(sys.argv) else 0
    main(args[0], args[1], show_pb=("--pb" in flags), len4=("--len4" in flags),
         client_ip=client, limit=limit)
    return 0


if __name__ == "__main__":
    sys.exit(_main())
