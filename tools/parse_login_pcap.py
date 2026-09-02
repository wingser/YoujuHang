# -*- coding: utf-8 -*-
"""解析 pcap 提取的登录请求/响应 H2 帧 + protobuf
用法: python tools/parse_login_pcap.py <bin> [--dump]
"""
import sys
import os
import base64
import glob

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

BASE = os.path.dirname(os.path.abspath(__file__))
PAYLOAD_DIR = os.path.normpath(os.path.join(BASE, "..", "captures", "payloads"))


def varint(buf, o):
    v = 0
    s = 0
    while True:
        x = buf[o]
        o += 1
        v |= (x & 0x7F) << s
        if not x & 0x80:
            break
        s += 7
    return v, o


def parse_proto(buf, indent=0, maxdepth=4):
    off = 0
    pad = "  " * indent
    while off < len(buf):
        try:
            tag, off = varint(buf, off)
        except Exception:
            print(pad + ".. end (truncated)")
            break
        fn, wt = tag >> 3, tag & 7
        try:
            if wt == 0:
                v, off = varint(buf, off)
                print(pad + "f%d varint = %d (0x%x)" % (fn, v, v))
            elif wt == 2:
                ln, off = varint(buf, off)
                v = buf[off:off + ln]
                off += ln
                if all(32 <= b < 127 for b in v) and v:
                    print(pad + "f%d str = %r" % (fn, v.decode("ascii", "replace")))
                else:
                    print(pad + "f%d bytes(%d) = %s" % (fn, len(v), v.hex()))
                    if indent < maxdepth and len(v) >= 2 and v[0] not in (0,):
                        parse_proto(v, indent + 1, maxdepth)
            elif wt == 5:
                v = buf[off:off + 4]
                off += 4
                print(pad + "f%d fixed32 = %s (le=%d)" % (fn, v.hex(), int.from_bytes(v, "little")))
            elif wt == 1:
                v = buf[off:off + 8]
                off += 8
                print(pad + "f%d fixed64 = %s" % (fn, v.hex()))
            else:
                print(pad + "f%d wire=%d <stop>" % (fn, wt))
                break
        except Exception as e:
            print(pad + ".. parse err: %s" % e)
            break


def parse_h2_frames(data, name):
    pos = 0
    idx = 0
    print("===== %s (%d bytes) =====" % (name, len(data)))
    while pos + 9 <= len(data):
        ln = int.from_bytes(data[pos:pos + 3], "big")
        typ = data[pos + 3]
        flg = data[pos + 4]
        sid = int.from_bytes(data[pos + 5:pos + 9], "big")
        payload = data[pos + 9:pos + 9 + ln]
        idx += 1
        print("[frame%d] type=%d flags=0x%x stream=%d len=%d" % (idx, typ, flg, sid, ln))
        if typ == 0x01:  # HEADERS
            print("   HPACK: %s" % payload.hex())
        elif typ == 0x00:  # DATA
            raw = payload
            txt = payload.decode("ascii", "replace")
            # 尝试 base64
            try:
                b64 = txt.strip()
                while len(b64) % 4:
                    b64 += "="
                dec = base64.b64decode(b64)
                print("   DATA b64 -> %d bytes" % len(dec))
                print("   protobuf:")
                parse_proto(dec, 2)
            except Exception as e:
                print("   DATA raw: %s (%s)" % (raw.hex(), e))
        elif typ in (0x04, 0x05, 0x06, 0x07, 0x08):
            print("   payload: %s" % payload.hex())
        pos += 9 + ln
        if ln == 0 and typ == 0x03:
            pos = pos  # RST_STREAM
    print("")


def main():
    files = sys.argv[1:] or sorted(glob.glob(os.path.join(PAYLOAD_DIR, "*.bin")))
    for f in files:
        with open(f, "rb") as fh:
            data = fh.read()
        parse_h2_frames(data, os.path.basename(f))


if __name__ == "__main__":
    main()
