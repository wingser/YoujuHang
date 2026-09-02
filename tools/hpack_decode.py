# -*- coding: utf-8 -*-
"""从 h5_xzone.log 提取所有 H2 HEADERS block，用 hpack 库完整解码（Huffman+动态表）"""
import io
import re
import sys
from hpack import Decoder

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

LOG = r"C:\Users\Lenovo\AppData\Local\Temp\h5_xzone.log"

def hexs_to_bytes(hs):
    try:
        # 去掉 ... (N bytes total) 尾部
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
        m = re.search(r"\[(\d+)\] (SEND|RECV) sock=(\d+) tag=ws2_32.dll!(WSASend|WSARecv|send|recv) bufCount=1 WSABUF.len=(\d+)", l)
        if m:
            t = int(m.group(1))
            dr = m.group(2)
            sock = int(m.group(3))
            if sock not in (3136, 2496):
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
                        ftype = b[off+3]
                        flags = b[off+4]
                        stream = (b[off+5] << 24) | (b[off+6] << 16) | (b[off+7] << 8) | b[off+8]
                        body = b[off+9:off+9+length]
                        events.append((t, sock, dr, stream, ftype, body))
                        off += 9 + length
                i = j
            else:
                i += 1
        else:
            i += 1

    events.sort(key=lambda e: e[0])
    print("H2 frames:", len(events))
    for t, sock, dr, stream, ftype, body in events:
        if sock not in decoders:
            decoders[sock] = Decoder()
        if ftype == 1:  # HEADERS
            dec = decoders[sock]
            try:
                headers = dec.decode(body)
                print("\n[%d] %s sock=%d stream=%d HEADERS len=%d" % (t, dr, sock, stream, len(body)))
                for k, v in headers:
                    print("   %s: %s" % (k, v))
            except Exception as e:
                print("[%d] %s sock=%d stream=%d HEADERS decode FAIL: %s" % (t, dr, sock, stream, e))
                print("   hex: %s" % body.hex())
        elif ftype == 0:
            pass
        elif ftype == 6:
            pass

if __name__ == "__main__":
    main()
