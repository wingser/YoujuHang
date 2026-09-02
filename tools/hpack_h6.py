# -*- coding: utf-8 -*-
"""解码 h6_xzone.log 中指定 sock 的 H2 HEADERS（HPACK + Huffman）
用法: python tools/hpack_h6.py [log_path] [sock]
"""
import io
import re
import sys
import base64
from hpack import Decoder

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

LOG = r"C:\Users\Lenovo\AppData\Local\Temp\h6_xzone.log"
SOCK = 4012
if len(sys.argv) > 1:
    LOG = sys.argv[1]
if len(sys.argv) > 2:
    SOCK = int(sys.argv[2])


def hexs_to_bytes(hs):
    try:
        idx = hs.find("...")
        if idx >= 0:
            hs = hs[:idx]
        return bytes.fromhex(hs.replace(" ", "").replace("\n", ""))
    except Exception:
        return None


def main():
    lines = io.open(LOG, encoding="utf-8", errors="replace").readlines()
    decoders = {}
    events = []
    i = 0
    while i < len(lines):
        l = lines[i]
        m = re.search(r"\[(\d+)\] (SEND|RECV) sock=(\d+)", l)
        if m:
            t = int(m.group(1))
            dr = m.group(2)
            sock = int(m.group(3))
            data_lines = []
            j = i + 1
            while j < len(lines) and not lines[j].startswith("["):
                data_lines.append(lines[j])
                j += 1
            raw = "".join(data_lines)
            b = hexs_to_bytes(raw)
            if b:
                off = 0
                while off + 9 <= len(b):
                    length = (b[off] << 16) | (b[off+1] << 8) | b[off+2]
                    if off + 9 + length > len(b):
                        break
                    ftype = b[off+3]
                    flags = b[off+4]
                    stream = (b[off+5] << 24) | (b[off+6] << 16) | (b[off+7] << 8) | b[off+8]
                    body = b[off+9:off+9+length]
                    events.append((t, sock, dr, stream, ftype, body))
                    off += 9 + length
            i = j
        else:
            i += 1

    events.sort(key=lambda e: e[0])
    print("H2 frames on sock=%d: %d" % (SOCK, sum(1 for e in events if e[1] == SOCK)))
    for t, sock, dr, stream, ftype, body in events:
        if sock != SOCK:
            continue
        if ftype not in (1, 0, 4):
            continue
        if sock not in decoders:
            decoders[sock] = Decoder()
        if ftype == 1:  # HEADERS
            dec = decoders[sock]
            try:
                headers = dec.decode(body)
                print("\n[%d] %s sock=%d stream=%d HEADERS" % (t, dr, sock, stream))
                for k, v in headers:
                    print("   %s: %s" % (k, v))
            except Exception as e:
                print("\n[%d] %s sock=%d stream=%d HEADERS decode FAIL: %s" % (t, dr, sock, stream, e))
                print("   hex: %s" % body.hex())
        elif ftype == 4:  # SETTINGS
            print("[%d] %s sock=%d SETTINGS %s" % (t, dr, sock, body.hex()))
        elif ftype == 0:
            info = "DATA len=%d" % len(body)
            if stream == 0 and len(body) == 0:
                info = "DATA(empty)"
            print("[%d] %s sock=%d stream=%d %s %s" % (t, dr, sock, stream, info, body.hex()[:160]))
            if len(body) > 0 and body[:1] not in (b"\x00",):
                try:
                    s = body.decode("ascii")
                    if re.fullmatch(r"[A-Za-z0-9+/_-]*={0,2}", s):
                        pad = (-len(s)) % 4
                        dec = base64.b64decode(s + "=" * pad, altchars=b"-_")
                        print("   b64-dec: %s" % dec.hex()[:200])
                except Exception:
                    pass


if __name__ == "__main__":
    main()
