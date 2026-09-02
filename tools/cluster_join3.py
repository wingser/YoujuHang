# -*- coding: utf-8 -*-
"""分析 join3 18141：按内容指纹聚类所有消息，输出低频/一次性消息（观战/上位动作）。
帧: [00 00 XX YY] [TT 00 00 07 CC] + 数据(XX字节), YY=01明文/TT=04, YY=00 base64/TT=01
"""
import base64, subprocess, sys, collections, struct
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

TSHARK = r'D:\Program Files\Wireshark\tshark.exe'
_argv = [a for a in sys.argv[1:] if not a.startswith('--')]
pcap = _argv[0] if _argv else r'D:\Git\YoujuHang\captures\join3_20260827_00001_20260827152918.pcapng'

def q(fields, filt):
    cmd = [TSHARK, '-r', pcap, '-Y', filt, '-T', 'fields'] + fields
    return subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8', errors='replace').stdout

def parse_varint(b, i):
    r, s = 0, 0
    while True:
        x = b[i]; i += 1
        r |= (x & 0x7F) << s
        if not (x & 0x80): return r, i
        s += 7

def pb_dump(b, indent=0, depth=0):
    lines, i = [], 0
    pad = '  ' * indent
    try:
        while i < len(b):
            key, i = parse_varint(b, i)
            field, wire = key >> 3, key & 7
            if wire == 0:
                v, i = parse_varint(b, i)
                lines.append(f'{pad}f{field}={v}')
            elif wire == 2:
                ln, i = parse_varint(b, i)
                d = b[i:i+ln]; i += ln
                try:
                    s = d.decode('utf-8')
                    if all(c == 10 or 32 <= ord(c) < 127 or ord(c) > 127 for c in s):
                        lines.append(f'{pad}f{field}="{s}"')
                        continue
                except Exception:
                    pass
                if depth < 5:
                    sub = pb_dump(d, indent+1, depth+1)
                    lines.append(f'{pad}f{field}={{')
                    lines.extend(sub)
                    lines.append(f'{pad}}}')
                else:
                    lines.append(f'{pad}f{field}=HEX{d.hex()[:40]}')
            else:
                lines.append(f'{pad}f{field} wire{wire}'); break
    except Exception as e:
        lines.append(f'{pad}!!{e}')
    return lines

rows = []
for line in q(['-e','frame.number','-e','frame.time_relative','-e','ip.src','-e','tcp.len','-e','tcp.payload'], 'tcp.port==18141 and tcp.len>0').splitlines():
    p = line.strip().split('\t')
    if len(p) != 5 or not p[4]: continue
    fno, t, src, ln, hexd = p
    raw = bytes.fromhex(hexd)
    direction = 'C->S' if src.startswith('192.') else 'S->C'
    rows.append((float(t), fno, direction, raw))

msgs = []
for t, fno, direction, raw in rows:
    i = 0
    while i + 4 <= len(raw):
        if raw[i:i+2] != b'\x00\x00':
            break
        xlen = raw[i+2]; flag = raw[i+3]
        if i + 9 + xlen > len(raw): break
        data = raw[i+9:i+9+xlen]
        msgs.append((t, fno, direction, flag, data))
        i += 9 + xlen

def decode(m):
    t, fno, direction, flag, data = m
    if flag == '01':
        return f'PLAIN hex={data.hex()}'
    try:
        b = base64.b64decode(data + b'=' * (-len(data) % 4))
    except Exception:
        return f'B64? hex={data.hex()[:60]}'
    return 'PB: ' + '; '.join(pb_dump(b, 0))[:200]

def fp(m):
    t, fno, direction, flag, data = m
    if flag == '01':
        return ('P', len(data), data[:4].hex())
    try:
        b = base64.b64decode(data + b'=' * (-len(data) % 4))
    except Exception:
        return ('Q', len(data), data[:8].hex())
    return ('B', len(data), b[:8].hex())

cl = collections.Counter()
for m in msgs: cl[fp(m)] += 1

# 输出低频类（<15次），带时间线
rare = {k for k, v in cl.items() if v < 15}
print('=== 低频/一次性消息 ===')
for t, fno, direction, flag, data in msgs:
    if fp((t, fno, direction, flag, data)) in rare:
        print(f'#{fno} t={t:8.3f} {direction} {decode((t,fno,direction,flag,data))}')
