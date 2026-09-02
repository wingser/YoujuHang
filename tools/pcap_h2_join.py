# -*- coding: utf-8 -*-
"""按 HTTP/2 stream 拼接 DATA 帧的 base64 文本，统一解码 + zlib + 打印 msgid。
用法: python pcap_h2_join.py <pcap> <port> [--dumpdir dir]
"""
import base64, collections, struct, sys, zlib
sys.stdout.reconfigure(encoding='utf-8')

def read_varint(b, i):
    val = 0; shift = 0
    while i < len(b):
        c = b[i]; i += 1
        val |= (c & 0x7f) << shift
        if not (c & 0x80): break
        shift += 7
    return val, i

def try_decompress(raw):
    for wbits in (zlib.MAX_WBITS, -zlib.MAX_WBITS, 16 + zlib.MAX_WBITS, 0):
        try:
            return zlib.decompress(raw, wbits), True
        except Exception:
            pass
    return raw, False

def main():
    import dpkt
    pcap_file, port = sys.argv[1], int(sys.argv[2])
    dumpdir = None
    if '--dumpdir' in sys.argv:
        dumpdir = sys.argv[sys.argv.index('--dumpdir') + 1]
    streams = collections.defaultdict(list)  # direction -> [(ts, data)]
    f = open(pcap_file, 'rb')
    try:
        reader = dpkt.pcapng.Reader(f)
    except Exception:
        f.close(); f = open(pcap_file, 'rb')
        reader = dpkt.pcap.Reader(f)
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
        sport, dport = tcp.sport, tcp.dport
        if port not in (sport, dport): continue
        data = bytes(tcp.data)
        if not data: continue
        if dport == port:
            direction = 'C2S'
        else:
            direction = 'S2C'
        streams[direction].append((ts, data))

    for direction in ('C2S', 'S2C'):
        if direction not in streams: continue
        pkts = sorted(streams[direction], key=lambda x: x[0])
        body = b"".join(d for _, d in pkts)
        idx = body.find(b"PRI * HTTP/2.0")
        if idx >= 0:
            body = body[idx + 24:]
        # 解码 H2 帧，DATA 按 stream 累积 base64 文本
        h2streams = collections.defaultdict(lambda: {'b64': '', 'time': 0.0, 'paths': []})
        off = 0
        dec = None
        try:
            from hpack import Decoder
            dec = Decoder()
        except Exception:
            pass
        while off + 9 <= len(body):
            length = (body[off] << 16) | (body[off+1] << 8) | body[off+2]
            ftype = body[off+3]
            stream = (body[off+5] << 24) | (body[off+6] << 16) | (body[off+7] << 8) | body[off+8]
            fb = body[off+9:off+9+length]
            if ftype == 1 and dec:
                try:
                    hs = dec.decode(fb)
                    d = {k: v for k, v in hs}
                    h2streams[stream]['paths'].append(d.get(':path', ''))
                except Exception:
                    pass
            elif ftype == 0:
                if fb and all(0x20 <= c < 0x7f for c in fb):
                    h2streams[stream]['b64'] += fb.decode('ascii', 'replace')
                    if h2streams[stream]['time'] == 0:
                        h2streams[stream]['time'] = 0
            off += 9 + length
            if off > len(body):
                break
        # 输出
        print("=" * 72)
        print(f"### {direction}  {len(h2streams)} streams")
        for st, d in sorted(h2streams.items()):
            b64 = d['b64']
            if not b64: continue
            b64_clean = b64.replace('-', '+').replace('_', '/')
            b64_clean = ''.join(c for c in b64_clean if c in 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789+/')
            try:
                raw = base64.b64decode(b64_clean + '=' * (-len(b64_clean) % 4))
            except Exception as e:
                print(f"stream={st} ERR {e} b64len={len(b64)}"); continue
            raw, was_comp = try_decompress(raw)
            if len(raw) < 2:
                print(f"stream={st} len<2"); continue
            mid = struct.unpack('<H', raw[:2])[0]
            body = raw[2:]
            print(f"stream={st} msgid={mid} len={len(raw)} comp={was_comp} paths={d['paths']} head={body[:24].hex()}")
            if dumpdir:
                fn = f"{dumpdir}/stream{st}_{direction}_{mid}.bin"
                open(fn, 'wb').write(body)
                print(f"    saved {fn}")

if __name__ == '__main__':
    main()
