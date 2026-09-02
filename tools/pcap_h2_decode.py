# -*- coding: utf-8 -*-
"""解析 pcapng 中指定端口（18000/18061）的 H2 连接：解码 HEADERS + DATA"""
import sys
import dpkt
import base64
from hpack import Decoder
sys.path.insert(0, r"d:\Git\YoujuHang\tools")
from proto_decode import parse_msg_str

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

def main():
    pcap = sys.argv[1]
    port = int(sys.argv[2]) if len(sys.argv) > 2 else 18000
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
        if eth.type not in (0x0800, 0x86dd):
            continue
        ip = eth.data
        if not isinstance(ip.data, dpkt.tcp.TCP):
            continue
        tcp = ip.data
        if tcp.sport != port and tcp.dport != port:
            continue
        key = (str(ip.src), str(ip.dst), tcp.sport, tcp.dport)
        streams.setdefault(key, []).append((ts, tcp.seq, tcp.flags, bytes(tcp.data)))
    for key, pkts in streams.items():
        src, dst, sport, dport = key
        pkts.sort(key=lambda x: x[1])
        data = b""
        for ts, seq, flags, d in pkts:
            data += d
        # 找到 H2 前奏（客户端流有，服务器流没有）
        idx = data.find(b"PRI * HTTP/2.0")
        print("=" * 70)
        print("stream %s:%d -> %s:%d len=%d (H2)" % (src, sport, dst, dport, len(data)))
        if idx >= 0:
            body = data[idx+24:]  # 跳过前奏
        else:
            # 服务器流：跳过 SETTINGS 帧头（可能被拆），从头解析
            body = data
        dec = Decoder()
        off = 0
        while off + 9 <= len(body):
            length = (body[off] << 16) | (body[off+1] << 8) | body[off+2]
            ftype = body[off+3]
            flags = body[off+4]
            stream = (body[off+5] << 24) | (body[off+6] << 16) | (body[off+7] << 8) | body[off+8]
            fb = body[off+9:off+9+length]
            tname = {0: "DATA", 1: "HEADERS", 2: "PRIORITY", 3: "RST_STREAM", 4: "SETTINGS", 6: "PING", 7: "GOAWAY", 8: "WINDOW_UPDATE", 9: "CONTINUATION"}.get(ftype, "?%d" % ftype)
            if ftype == 1:
                try:
                    hs = dec.decode(fb)
                    print("  [%s] HEADERS stream=%d" % (tname, stream))
                    for k, v in hs:
                        print("      %s: %s" % (k, v))
                except Exception as e:
                    print("  HEADERS decode fail: %s" % e)
            elif ftype == 0:
                # DATA：尝试 base64
                try:
                    if fb and all(0x20 <= c < 0x7f for c in fb):
                        s = fb.decode().replace("-", "+").replace("_", "/")
                        s += "=" * (-len(s) % 4)
                        fb = base64.b64decode(s)
                        print("  [%s] DATA stream=%d len=%d (base64->%d)" % (tname, stream, length, len(fb)))
                    else:
                        print("  [%s] DATA stream=%d len=%d" % (tname, stream, length))
                    print("      hex: %s" % fb.hex())
                    if len(fb) >= 2:
                        mid = (fb[0] << 8) | fb[1]
                        print("      prefix[0:2] LE=%d" % mid)
                        try:
                            print("      proto[2:]: %s" % parse_msg_str(fb[2:]))
                        except Exception as e:
                            print("      parse err:", e)
                except Exception as e:
                    print("  DATA err:", e)
            else:
                print("  [%s] stream=%d len=%d" % (tname, stream, length))
            off += 9 + length
            if off > len(body):
                print("  ... truncated")
                break

if __name__ == "__main__":
    main()
