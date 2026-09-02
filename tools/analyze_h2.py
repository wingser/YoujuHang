# -*- coding: utf-8 -*-
"""TCP 层重组 HTTP/2 帧，按 http2 stream 重组请求/响应，解码 base64url 数据。
用法: analyze_h2.py <pcap> <tcp_stream> [max_segments] [max_resp_bytes]
"""
import base64, struct, sys, subprocess, json
from collections import defaultdict
sys.stdout.reconfigure(encoding='utf-8')

def b64d(s):
    s = s.strip()
    return base64.urlsafe_b64decode(s + '=' * (-len(s) % 4))

def parse_varint(b, i):
    result, shift = 0, 0
    while True:
        if i >= len(b): raise ValueError
        byte = b[i]; i += 1
        result |= (byte & 0x7F) << shift
        if not (byte & 0x80): break
        shift += 7
    return result, i

def decode_pb(b, indent=0, maxdepth=10):
    lines, i = [], 0
    pad = '  ' * indent
    while i < len(b):
        try:
            key, i = parse_varint(b, i)
        except Exception:
            lines.append(f'{pad}!! overrun @{i}'); break
        field, wire = key >> 3, key & 7
        if wire == 0:
            try:
                val, i = parse_varint(b, i)
            except Exception:
                lines.append(f'{pad}!! varint overrun'); break
            lines.append(f'{pad}f{field} = {val}')
        elif wire == 1:
            if i + 8 > len(b): lines.append(f'{pad}!! short64'); break
            val = struct.unpack('<Q', b[i:i+8])[0]; i += 8
            lines.append(f'{pad}f{field} = 0x{val:016x}')
        elif wire == 2:
            try:
                ln, i = parse_varint(b, i)
            except Exception:
                lines.append(f'{pad}!! len overrun'); break
            if i + ln > len(b):
                lines.append(f'{pad}!! wire2 short'); break
            data = b[i:i+ln]; i += ln
            try:
                s = data.decode('utf-8')
                if all(ord(c) == 10 or 32 <= ord(c) < 127 or ord(c) > 127 for c in s):
                    lines.append(f'{pad}f{field} = "{s}"'); continue
            except Exception:
                pass
            try:
                if indent < maxdepth:
                    sub = decode_pb(data, indent+1, maxdepth)
                    lines.append(f'{pad}f{field} = {{'); lines.extend(sub); lines.append(f'{pad}}}')
                else:
                    raise ValueError
            except Exception:
                lines.append(f'{pad}f{field} = HEX {data[:80].hex()}')
        elif wire == 5:
            if i + 4 > len(b): lines.append(f'{pad}!! short32'); break
            val = struct.unpack('<I', b[i:i+4])[0]; i += 4
            lines.append(f'{pad}f{field} = 0x{val:08x}')
        else:
            # 跳过未知 wire（尝试按 varint 推进）
            try:
                _, i = parse_varint(b, i)
            except Exception:
                lines.append(f'{pad}!! wire{wire} overrun'); break
    return lines

TSHARK = r'D:\Program Files\Wireshark\tshark.exe'
PCAP = sys.argv[1] if len(sys.argv) > 1 else r'D:\Git\YoujuHang\captures\team_20260827111730_00001_20260827111732.pcapng'
TCP_STREAM = sys.argv[2] if len(sys.argv) > 2 else '20'

cmd = [TSHARK, '-r', PCAP, '-Y', f'tcp.stream=={TCP_STREAM} && tcp.len>0',
       '-T', 'fields', '-e', 'frame.time_relative', '-e', 'ip.src', '-e', 'tcp.len', '-e', 'tcp.payload']
out = subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8', errors='replace').stdout

# 重组每方向 TCP 数据
from collections import OrderedDict
streams = OrderedDict()  # (src) -> list of (time, bytes)
for line in out.splitlines():
    p = line.strip().split('\t')
    if len(p) < 4 or not p[3]: continue
    t, src, ln, hd = p[0], p[1], p[2], p[3]
    try:
        data = bytes.fromhex(hd)
    except Exception:
        continue
    streams.setdefault(src, []).append((float(t), data))

# 解析 HTTP/2 帧
PREFACE = b'PRI * HTTP/2.0\r\n\r\nSM\r\n\r\n'

def parse_h2(segments):
    """返回 [(time, dir, stream_id, type, flags, payload)]"""
    buf = b''
    frames = []
    for t, d in segments:
        buf += d
        while True:
            if buf.startswith(PREFACE):
                buf = buf[len(PREFACE):]
                continue
            if len(buf) < 9:
                break
            ln = int.from_bytes(buf[0:3], 'big')
            if len(buf) < 9 + ln:
                break
            typ = buf[3]; flags = buf[4]
            sid = int.from_bytes(buf[5:9], 'big') & 0x7FFFFFFF
            payload = buf[9:9+ln]
            frames.append((t, typ, flags, sid, payload))
            buf = buf[9+ln:]
    return frames

frames = parse_h2(streams[next(iter(streams))] if streams else [])

def reassemble():
    """按 http2 stream 重组请求与响应。返回 [(t, 'req'/'rsp', sid, headers, body)]"""
    # 需要双向帧
    all_frames = []
    for src, segs in streams.items():
        for t, d in segs:
            all_frames.append((t, src, d))
    all_frames.sort(key=lambda x: x[0])
    buf = {s: b'' for s in streams}
    cur = {}
    out = []
    # 简化：解析每个方向为帧列表
    dir_frames = {}
    for src, segs in streams.items():
        fl = parse_h2(segs)
        dir_frames[src] = fl
    # 合并双向帧按时间
    merged = []
    for src, fl in dir_frames.items():
        for t, typ, flags, sid, payload in fl:
            merged.append((t, src, typ, flags, sid, payload))
    merged.sort(key=lambda x: x[0])
    # 提取 method/path（用 tshark http2.headers 字段关联 sid）
    import subprocess as sp
    cmdh = [TSHARK, '-r', PCAP, '-Y', f'tcp.stream=={TCP_STREAM} && http2.headers',
            '-T', 'fields', '-e', 'http2.stream', '-e', 'http2.headers.method', '-e', 'http2.headers.path']
    meta = {}
    for line in sp.run(cmdh, capture_output=True, text=True, encoding='utf-8', errors='replace').stdout.splitlines():
        pp = line.strip().split('\t')
        if len(pp) >= 3 and pp[1] and pp[2]:
            meta[pp[0]] = (pp[1], pp[2])
    pending = {}  # sid -> {'req': bool, 'buf': bytes, 't': t}
    for t, src, typ, flags, sid, payload in merged:
        if typ == 0:  # DATA only
            if sid not in pending:
                is_req = src.startswith('192.')
                pending[sid] = {'req': is_req, 'buf': b'', 't': t}
            pending[sid]['buf'] += payload
            if flags & 1:  # END_STREAM
                p = pending.pop(sid)
                m = meta.get(str(sid), ('', ''))
                yield (p['t'], 'req' if p['req'] else 'rsp', sid, p['buf'], m[0], m[1])
    # 未闭合的
    for sid, p in pending.items():
        if p['buf']:
            m = meta.get(str(sid), ('', ''))
            yield (p['t'], 'req' if p['req'] else 'rsp', sid, p['buf'], m[0], m[1])

def decode_body(data):
    """尝试解码请求/响应 body。返回 (desc, lines)"""
    try:
        ascii_s = data.decode('ascii')
        if len(ascii_s) == len(data) and len(ascii_s) >= 4 and all(c in 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_+/=' for c in ascii_s):
            raw = b64d(ascii_s)
            # zlib 压缩 (0x78 0x9c) ?
            if len(raw) > 2 and raw[0] == 0x78 and raw[1] in (0x9c, 0x01, 0xda):
                try:
                    import zlib
                    d = zlib.decompress(raw)
                    # 解压后可能还有一层 base64
                    try:
                        s2 = d.decode('ascii')
                        if len(s2) == len(d) and len(s2) >= 8 and all(c in 'ABCDEFGHIJKLMNOPQRSTUVWXYZabcdefghijklmnopqrstuvwxyz0123456789-_+/=' for c in s2):
                            d2 = b64d(s2)
                            if len(d2) >= 2:
                                return f'zlib({len(raw)}B)->b64->{len(d2)}B', decode_pb(d2)
                    except Exception:
                        pass
                    if len(d) >= 2:
                        key = d[0]; field = key >> 3; wire = key & 7
                        if field >= 1 and wire in (0, 1, 2, 5):
                            return f'zlib({len(raw)}B)->pb({len(d)}B)', decode_pb(d)
                    return f'zlib({len(raw)}B)->{len(d)}B HEX {d[:32].hex()}', [f'ascii: {d[:64]!r}']
                except Exception:
                    pass
            if len(raw) >= 2:
                key = raw[0]
                field = key >> 3
                wire = key & 7
                if field >= 1 and wire in (0, 1, 2, 5):
                    return f'protobuf (纯, {len(raw)}B)', decode_pb(raw)
                else:
                    mid = struct.unpack('<H', raw[:2])[0]
                    return f'[MsgID={mid}] ({len(raw)}B)', decode_pb(raw[2:])
    except Exception:
        pass
    return 'HEX', [data[:80].hex()]

print(f'TCP stream {TCP_STREAM} 重组结果:')
for t, kind, sid, body, method, path in reassemble():
    desc, lines = decode_body(body)
    rp = f' {method} {path}' if path else ''
    print(f'\n--- [{kind}] h2s={sid} t={t:.3f}{rp} {desc} ---')
    for l in lines[:24]:
        print(' ', l)
    if len(lines) > 24:
        print(f'  ... ({len(lines)-24} more lines)')
