# -*- coding: utf-8 -*-
"""分析 join3 18141：C->S 请求消息全解，按内容分组 + 时间线，找观战/上位请求。
"""
import base64, subprocess, sys, collections
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
                if depth < 4:
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

# 只取 C->S 的 base64 消息
cs = [(t,fno,d,fl,data) for (t,fno,d,fl,data) in msgs if d == 'C->S' and fl == 0]
print(f'C->S base64 消息数: {len(cs)}')

# 分组：用解码后完整 protobuf 做指纹（去掉变化字段：时间戳等）
def b64d(data):
    s = data
    try:
        return base64.b64decode(s + b'=' * (-len(s) % 4))
    except Exception:
        try:
            return base64.urlsafe_b64decode(s + b'=' * (-len(s) % 4))
        except Exception:
            return None

def sig(data):
    b = b64d(data)
    if b is None:
        return ('undec', data[:16].hex())
    # 指纹 = 字段号集合 + 每个字段 wire
    i, parts = 0, []
    try:
        while i < len(b):
            key, i = parse_varint(b, i)
            f, w = key >> 3, key & 7
            if w == 0:
                _, i = parse_varint(b, i)
                parts.append(f'{f}:v')
            elif w == 2:
                ln, i = parse_varint(b, i)
                d = b[i:i+ln]; i += ln
                # 若是嵌套则递归
                parts.append(f'{f}:b{len(d)}')
            else:
                parts.append(f'{f}:w{w}'); break
    except Exception:
        parts.append('END')
    return tuple(parts)

groups = collections.defaultdict(list)
for m in cs:
    groups[sig(m[4])].append(m)

print(f'分组数: {len(groups)}')
for gi, (s, items) in enumerate(sorted(groups.items(), key=lambda kv: -len(kv[1]))):
    print(f'\n--- 组{gi} 频次={len(items)} sig={s}')
    for t, fno, d, fl, data in items[:3]:
        print(f'  #{fno} t={t:8.3f} data={data[:40]}...')
        b = b64d(data)
        if b is None:
            print('    (base64 decode failed)')
            continue
        for l in pb_dump(b, 4)[:14]:
            print(l)
    if len(items) > 3:
        # 打印该组全部时间点
        ts = ' '.join(f'#{fno}@{t:.1f}' for t, fno, *_ in items)
        print(f'  全部时间点: {ts}')
