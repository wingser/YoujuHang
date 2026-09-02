# -*- coding: utf-8 -*-
"""解析 pcap 中某 IP 的 HTTP/2 帧序列：HEADERS/DATA/PING 解码输出"""
import base64
import subprocess
import sys

TSHARK = r"D:\Program Files\Wireshark\tshark.exe"

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def parse_frames(raw):
    pos = 0
    if raw.startswith(b"PRI *"):
        pos = 24
    while pos + 9 <= len(raw):
        length = (raw[pos] << 16) | (raw[pos + 1] << 8) | raw[pos + 2]
        ftype = raw[pos + 3]
        flags = raw[pos + 4]
        stream = int.from_bytes(raw[pos + 5:pos + 9], "big")
        body = raw[pos + 9:pos + 9 + length]
        if pos + 9 + length > len(raw):
            yield ("PARTIAL", stream, flags, raw[pos + 9:])
            break
        yield (ftype, stream, flags, body)
        pos += 9 + length


def try_b64(body):
    try:
        s = body.decode("ascii")
    except UnicodeDecodeError:
        return None
    if s and len(s) >= 8 and len(s) % 4 == 0 and \
            all(c in "ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/=" for c in s):
        return base64.b64decode(s)
    return None


def main(pcap, ip):
    out = subprocess.run([TSHARK, "-r", pcap,
                          "-Y", "tcp.payload",
                          "-T", "fields", "-e", "frame.time_relative",
                          "-e", "ip.src", "-e", "tcp.srcport",
                          "-e", "ip.dst", "-e", "tcp.dstport",
                          "-e", "tcp.payload"],
                         capture_output=True).stdout.decode("utf-8", "replace")
    # 按流分组
    streams = {}
    order = []
    for ln in out.splitlines():
        parts = ln.split("\t")
        if len(parts) < 6:
            continue
        src, dst = parts[1], parts[3]
        if ip not in (src, dst):
            continue
        key = (src, parts[2], dst, parts[4])
        if key not in streams:
            streams[key] = []
            order.append(key)
        streams[key].append(parts)
    for key in order:
        print("=" * 80)
        print("flow %s:%s -> %s:%s" % key)
        buf = b""
        partial = b""
        for parts in streams[key]:
            t = parts[0]
            data = bytes.fromhex(parts[5])
            buf = partial + data
            pos = 0
            if buf.startswith(b"PRI *"):
                pos = 24
            consumed = 0
            while True:
                if pos + 9 > len(buf):
                    break
                length = (buf[pos] << 16) | (buf[pos + 1] << 8) | buf[pos + 2]
                if pos + 9 + length > len(buf):
                    break
                ftype = buf[pos + 3]
                flags = buf[pos + 4]
                stream = int.from_bytes(buf[pos + 5:pos + 9], "big")
                body = buf[pos + 9:pos + 9 + length]
                desc = fmt_frame(t, ftype, stream, flags, body)
                if desc:
                    print(desc)
                pos += 9 + length
                consumed = pos
            partial = buf[consumed:]
        if partial:
            print("  [partial buffer %d bytes]" % len(partial))


def fmt_frame(t, ftype, stream, flags, body):
    if ftype == 0x0:  # DATA
        dec = try_b64(body)
        if dec is not None:
            return "t=%-9s DATA     stream=%d flags=0x%02x len=%d b64-> %s" % (
                t, stream, flags, len(body), dec[:96].hex())
        return "t=%-9s DATA     stream=%d flags=0x%02x len=%d hex=%s" % (
            t, stream, flags, len(body), body[:48].hex())
    if ftype == 0x1:  # HEADERS
        return "t=%-9s HEADERS  stream=%d flags=0x%02x len=%d cipher=%s" % (
            t, stream, flags, len(body), body[:64].hex())
    if ftype == 0x4:  # SETTINGS
        return "t=%-9s SETTINGS stream=%d flags=0x%02x len=%d" % (t, stream, flags, len(body))
    if ftype == 0x6:  # PING
        return "t=%-9s PING     stream=%d flags=0x%02x payload=%s" % (t, stream, flags, body.hex())
    if ftype == 0x7:  # GOAWAY
        return "t=%-9s GOAWAY   stream=%d flags=0x%02x payload=%s" % (t, stream, flags, body.hex())
    if ftype == 0x8:  # WINDOW_UPDATE
        return "t=%-9s WIN_UP   stream=%d flags=0x%02x payload=%s" % (t, stream, flags, body.hex())
    return "t=%-9s TYPE0x%02x stream=%d flags=0x%02x len=%d" % (t, ftype, stream, flags, len(body))


if __name__ == "__main__":
    main(sys.argv[1], sys.argv[2])
