# -*- coding: utf-8 -*-
"""解析 join3 18141 消息 msgid 序列：按时间列出 C->S 消息的 msgid（2B LE）+ protobuf 摘要。
帧: [00 00 XX YY] [TT 00 00 07 CC] + 数据(XX字节), YY=01明文, YY=00 base64
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

def pb_sig(b):
    """返回 protobuf 顶层字段签名"""
    i, parts = 0, []
    try:
        while i < len(b):
            key, i = parse_varint(b, i)
            f, w = key >> 3, key & 7
            if w == 0:
                v, i = parse_varint(b, i)
                parts.append(f'{f}={v}')
            elif w == 2:
                ln, i = parse_varint(b, i)
                d = b[i:i+ln]; i += ln
                try:
                    s = d.decode('utf-8')
                    if all(c == 10 or 32 <= ord(c) < 127 or ord(c) > 127 for c in s):
                        parts.append(f'{f}="{s[:24]}"')
                        continue
                except Exception:
                    pass
                parts.append(f'{f}({len(d)}B)')
            else:
                parts.append(f'{f}w{w}'); break
    except Exception:
        parts.append('END')
    return ' '.join(parts)

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

print('=== C->S 消息 msgid 序列（按时间，抽样展示变化）===')
prev_key = None
shown = 0
for t, fno, d, fl, data in msgs:
    if d != 'C->S':
        continue
    if fl == 1:
        key = ('PLAIN', data.hex()[:8])
    else:
        b = b64d(data)
        if b is None or len(b) < 2:
            key = ('UNDEC', data.hex()[:8])
        else:
            mid = b[0] | (b[1] << 8)
            key = (mid, pb_sig(b[2:])[:70])
    if key != prev_key:
        if shown < 60:
            print(f'#{fno} t={t:8.3f} key={key}')
            shown += 1
        prev_key = key

# 每种 msgid 统计
cnt = collections.Counter()
for t, fno, d, fl, data in msgs:
    if d != 'C->S':
        continue
    if fl == 1:
        cnt['PLAIN:' + data.hex()[:8]] += 1
    else:
        b = b64d(data)
        if b is None or len(b) < 2:
            cnt['UNDEC'] += 1
        else:
            mid = b[0] | (b[1] << 8)
            cnt[f'M{mid}'] += 1
print('\n=== C->S msgid 计数 ===')
for k, v in cnt.most_common():
    print(f'{v:5d}  {k}')
