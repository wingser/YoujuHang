# -*- coding: utf-8 -*-
"""解析 pcap 中指定 IP 的 HTTP/2 帧序列，并把 DATA 载荷解到 protobuf。

游聚的 /login/ /game/ /zone/2/ 都是 HTTP/2 明文通道：
    DATA = base64url( [2B MsgID LE] + protobuf )     # 请求
    DATA = base64url( protobuf ) 或 base64url(zlib(protobuf))  # 响应

用法:
    python h2_flow.py <pcap> <ip>                 全部流
    python h2_flow.py <pcap> <ip> --pb            展开 protobuf
    python h2_flow.py <pcap> <ip> --only-data     只看 DATA 帧
    python h2_flow.py <pcap> <ip> --client 192.168.1.9   指定客户端 IP 判定方向

依赖: tshark（见 capture.py 的查找逻辑）
"""
import base64
import os
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

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
try:
    from capture import find_tshark
    TSHARK = find_tshark()
except Exception:
    TSHARK = r"D:\Program Files\Wireshark\tshark.exe"

B64_CHARS = set("ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=-_")


def b64_decode(body):
    """尝试标准 / URL-safe 两种 base64，自动补 padding。失败返回 None。"""
    try:
        s = body.decode("ascii")
    except Exception:
        return None
    if not s or len(s) < 4 or any(c not in B64_CHARS for c in s):
        return None
    for decoder in (base64.urlsafe_b64decode, base64.b64decode):
        for pad in ("", "=", "==", "==="):
            try:
                return decoder(s + pad)
            except Exception:
                continue
    return None


def try_inflate(data):
    """DATA 载荷可能被 zlib 压缩，尝试解压"""
    import zlib
    for wbits in (15, -15, 47):
        try:
            return zlib.decompress(data, wbits)
        except Exception:
            continue
    return None


def fmt_proto(data, indent=4, maxlines=14):
    if decode is None or not data:
        return ""
    pad = " " * indent
    try:
        lines = dumps(decode(data, maxdepth=6), show_hex=48)
    except Exception as e:
        return "%s<proto decode failed: %s>" % (pad, e)
    out = []
    for l in lines[:maxlines]:
        out.append(pad + l)
    if len(lines) > maxlines:
        out.append("%s... (%d 行)" % (pad, len(lines)))
    return "\n".join(out)


def frame_name(t):
    return {0x0: "DATA", 0x1: "HEADERS", 0x2: "PRIORITY", 0x3: "RST_STREAM",
            0x4: "SETTINGS", 0x5: "PUSH_PROMISE", 0x6: "PING", 0x7: "GOAWAY",
            0x8: "WINDOW_UPDATE", 0x9: "CONTINUATION"}.get(t, "TYPE0x%02x" % t)


def parse_h2(buf):
    """切分 HTTP/2 帧。返回 (帧列表, 剩余未处理字节)"""
    frames = []
    pos = 0
    if buf.startswith(b"PRI *"):
        pos = 24  # 连接前言
    while pos + 9 <= len(buf):
        length = (buf[pos] << 16) | (buf[pos + 1] << 8) | buf[pos + 2]
        if pos + 9 + length > len(buf):
            break
        ftype = buf[pos + 3]
        flags = buf[pos + 4]
        stream = int.from_bytes(buf[pos + 5:pos + 9], "big") & 0x7FFFFFFF
        body = buf[pos + 9:pos + 9 + length]
        frames.append((ftype, stream, flags, body))
        pos += 9 + length
    return frames, buf[pos:]


def main(pcap, ip, show_pb=False, only_data=False, client_ip=None, maxlines=14):
    # 用 tcp.stream 做分组键：一条 TCP 连接一个流，两个方向的报文按时间合并展示。
    # 比按 (src,sport,dst,dport) 分组更正确——后者会把一条连接拆成两条，且方向难判。
    cmd = [TSHARK, "-r", pcap, "-Y", "tcp.payload", "-T", "fields",
           "-e", "frame.number", "-e", "frame.time_relative",
           "-e", "tcp.stream", "-e", "tcp.flags",
           "-e", "ip.src", "-e", "tcp.srcport",
           "-e", "ip.dst", "-e", "tcp.dstport", "-e", "tcp.payload"]
    out = subprocess.run(cmd, capture_output=True).stdout.decode("utf-8", "replace")

    # 单独查 SYN：tcp.payload 过滤会排除掉没有载荷的握手包，
    # 而 SYN 的发送方是判定客户端/服务端方向唯一可靠的依据。
    syn_cmd = [TSHARK, "-r", pcap,
               "-Y", "tcp.flags.syn==1 && tcp.flags.ack==0",
               "-T", "fields", "-e", "tcp.stream", "-e", "ip.src"]
    syn_out = subprocess.run(syn_cmd, capture_output=True).stdout.decode("utf-8", "replace")
    client_of = {}
    for ln in syn_out.splitlines():
        parts = ln.split("\t")
        if len(parts) >= 2 and parts[0] and parts[1]:
            client_of.setdefault(parts[0], parts[1])

    flows = {}   # stream_id -> {"pkts": [...], "client": ip, "endpoints": set}
    order = []
    for ln in out.splitlines():
        p = ln.split("\t")
        if len(p) < 9:
            continue
        fno, t, sid, tflags, src, sp, dst, dp, hexd = p[:9]
        if ip and ip not in (src, dst):
            continue
        if sid not in flows:
            flows[sid] = {"pkts": [], "client": client_of.get(sid), "eps": []}
            order.append(sid)
        flows[sid]["pkts"].append(p)
        flows[sid]["eps"].append("%s:%s" % (src, sp))
        flows[sid]["eps"].append("%s:%s" % (dst, dp))

    if not order:
        print("未找到涉及 %s 的 TCP 载荷，检查 IP 或 pcap 路径" % ip)
        return

    for sid in order:
        f = flows[sid]
        client = client_ip or f["client"]
        eps = sorted(set(f["eps"]))
        print("=" * 78)
        print("tcp.stream=%s  %s  (%d packets)  client=%s"
              % (sid, " <-> ".join(eps[:2]), len(f["pkts"]), client or "未知"))
        buf = b""
        for p in f["pkts"]:
            fno, t, sid2, tflags, src, sp, dst, dp, hexd = p[:9]
            try:
                buf += bytes.fromhex(hexd)
            except Exception:
                continue
            frames, buf = parse_h2(buf)
            for ftype, stream, flags, body in frames:
                if only_data and ftype != 0x0:
                    continue
                d = "  ? "
                if client:
                    d = "C->S" if src == client else "S->C"
                name = frame_name(ftype)
                if ftype == 0x0:
                    dec = b64_decode(body)
                    if dec is None:
                        print("t=%-10s %s DATA     h2stream=%-3d flags=0x%02x len=%-5d hex=%s"
                              % (t, d, stream, flags, len(body), body[:48].hex()))
                        continue
                    inf = try_inflate(dec)
                    tag = "zlib->" if inf else ""
                    payload = inf if inf else dec
                    # 请求体开头常有 2 字节 MsgID（小端）
                    msgid = None
                    pb = payload
                    if len(payload) >= 2:
                        msgid = payload[0] | (payload[1] << 8)
                        # 仅当第 3 字节像 protobuf tag 时才认定有 MsgID 前缀
                        if len(payload) > 2 and (payload[2] & 0x78) and (payload[2] & 7) in (0, 1, 2, 5):
                            pb = payload[2:]
                        else:
                            msgid = None
                    head = "t=%-10s %s DATA     h2stream=%-3d flags=0x%02x b64len=%-5d %spayload=%dB" % (
                        t, d, stream, flags, len(body), tag, len(payload))
                    if msgid is not None:
                        head += " msgid=%d(0x%04x)" % (msgid, msgid)
                    print(head)
                    if show_pb:
                        s = fmt_proto(pb, 4, maxlines)
                        if s:
                            print(s)
                elif ftype == 0x1:
                    print("t=%-10s %s HEADERS  h2stream=%-3d flags=0x%02x len=%-3d %s"
                          % (t, d, stream, flags, len(body), body[:64].hex()))
                elif ftype in (0x4, 0x6, 0x7, 0x8):
                    print("t=%-10s %s %-8s h2stream=%-3d flags=0x%02x payload=%s"
                          % (t, d, name, stream, flags, body[:32].hex()))
                else:
                    print("t=%-10s %s %-8s h2stream=%-3d flags=0x%02x len=%d"
                          % (t, d, name, stream, flags, len(body)))
        if buf:
            print("  [残留 %d 字节，可能跨包未重组完整]" % len(buf))


def _main():
    args = [a for a in sys.argv[1:] if not a.startswith("--")]
    flags = set(a for a in sys.argv[1:] if a.startswith("--"))
    if len(args) < 2:
        print(__doc__)
        return 1
    client = None
    if "--client" in flags:
        i = sys.argv.index("--client")
        client = sys.argv[i + 1] if i + 1 < len(sys.argv) else None
    main(args[0], args[1], show_pb=("--pb" in flags),
         only_data=("--only-data" in flags), client_ip=client)
    return 0


if __name__ == "__main__":
    sys.exit(_main())
