# -*- coding: utf-8 -*-
"""解析 join3 18141：低频消息(msgid 515/516/1026/1027/1032/1033/1080)的完整时间线与内容。
"""
import base64, subprocess, sys
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
                    lines.append(f'{pad}f{field}=HEX{d.hex()[:48]}')
            else:
                lines.append(f'{pad}f{field} wire{wire} raw={b[i-1:i+8].hex()}'); break
    except Exception as e:
        lines.append(f'{pad}!!{e}')
    return lines

def b64d(data):
    s = data
    try:
        return base64.b64decode(s + b'=' * (-len(s) % 4))
    except Exception:
        try:
            return base64.urlsafe_b64decode(s + b'=' * (-len(s) % 4))
        except Exception:
            return None

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

INTEREST = {515, 516, 1026, 1027, 1032, 1033, 1080}
print('=== 低频消息（含观战/上位动作候选）时间线 ===')
for t, fno, d, fl, data in msgs:
    if d != 'C->S': continue
    if fl == 1:
        continue
    b = b64d(data)
    if b is None or len(b) < 2: continue
    mid = b[0] | (b[1] << 8)
    if mid in INTEREST:
        print(f'\n#{fno} t={t:8.3f} C->S msgid={mid} b64len={len(data)}')
        print(f'  raw: {data.decode("latin1", errors="replace")}')
        for l in pb_dump(b[2:], 2)[:20]:
            print(l)
