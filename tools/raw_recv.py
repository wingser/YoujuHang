# -*- coding: utf-8 -*-
"""提取指定时间的 RECV 完整原始数据并解析 H2 帧"""
import io
import re
import sys
import base64

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

LOG = r"C:\Users\Lenovo\AppData\Local\Temp\h5_xzone.log"

def main():
    t0 = int(sys.argv[1]) if len(sys.argv) > 1 else 24590552
    lines = io.open(LOG, encoding="utf-8", errors="replace").readlines()
    i = 0
    while i < len(lines):
        l = lines[i]
        m = re.search(r"\[(\d+)\] (SEND|RECV) sock=(\d+) tag=ws2_32.dll!(WSASend|WSARecv|send|recv) bufCount=1 WSABUF.len=(\d+)", l)
        if m:
            t = int(m.group(1))
            if t == t0:
                dr = m.group(2)
                sock = int(m.group(3))
                data_lines = []
                j = i + 1
                while j < len(lines) and not lines[j].startswith("["):
                    data_lines.append(lines[j])
                    j += 1
                raw = "".join(data_lines)
                idx = raw.find("...")
                if idx >= 0:
                    raw = raw[:idx]
                b = bytes.fromhex(raw.replace(" ", "").replace("\n", ""))
                print("[%d] %s sock=%d total=%d bytes" % (t, dr, sock, len(b)))
                off = 0
                while off + 9 <= len(b):
                    length = (b[off] << 16) | (b[off+1] << 8) | b[off+2]
                    ftype = b[off+3]
                    flags = b[off+4]
                    stream = (b[off+5] << 24) | (b[off+6] << 16) | (b[off+7] << 8) | b[off+8]
                    body = b[off+9:off+9+length]
                    tname = {0: "DATA", 1: "HEADERS", 2: "PRIORITY", 3: "RST_STREAM", 4: "SETTINGS", 5: "PUSH_PROMISE", 6: "PING", 7: "GOAWAY", 8: "WINDOW_UPDATE", 9: "CONTINUATION"}.get(ftype, "?%d" % ftype)
                    print("  frame: %s len=%d flags=0x%02x stream=%d" % (tname, length, flags, stream))
                    if ftype == 0:
                        # DATA body
                        if body and all(0x20 <= c < 0x7f for c in body):
                            s = body.decode().replace("-", "+").replace("_", "/")
                            try:
                                dec = base64.b64decode(s + "=" * (-len(s) % 4))
                                print("    DATA(base64) -> %d bytes: %s" % (len(dec), dec.hex()))
                            except Exception as e:
                                print("    DATA(base64) decode err %s" % e)
                                print("    DATA text: %s" % body.decode(errors="replace"))
                        else:
                            print("    DATA: %s" % body.hex())
                    elif ftype == 1:
                        print("    HEADERS hex: %s" % body.hex())
                    else:
                        print("    body: %s" % body.hex())
                    off += 9 + length
                break
            i += 1
        else:
            i += 1

if __name__ == "__main__":
    main()
