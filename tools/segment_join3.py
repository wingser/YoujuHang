# -*- coding: utf-8 -*-
"""按时间段统计 join3 18141 的 C->S 消息模式，对比：
   t<134 建房期 / 134-141 离房 / 141-202 观战期 / 202-633 上位后
"""
import base64, subprocess, sys, collections
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

TSHARK = r'D:\Program Files\Wireshark\tshark.exe'
_argv = [a for a in sys.argv[1:] if not a.startswith('--')]
pcap = _argv[0] if _argv else r'D:\Git\YoujuHang\captures\join3_20260827_00001_20260827152918.pcapng'

def q(fields, filt):
    cmd = [TSHARK, '-r', pcap, '-Y', filt, '-T', 'fields'] + fields
    return subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8', errors='replace').stdout

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

# 分段
segs = [
    ('A 建房期 t<134', 0, 134),
    ('B 离房-进房 134-141.3', 134, 141.3),
    ('C 观战期 141.3-202.6', 141.3, 202.6),
    ('D 上位后 202.6-633', 202.6, 1e9),
]

def mid_of(m):
    t, fno, d, fl, data = m
    if d != 'C->S': return None
    if fl == 1:
        return 'PLAIN'
    b = b64d(data)
    if b is None or len(b) < 2: return 'UNDEC'
    return b[0] | (b[1] << 8)

for name, t0, t1 in segs:
    cnt = collections.Counter()
    tmin, tmax = None, None
    for m in msgs:
        t = m[0]
        if t0 <= t < t1:
            mid = mid_of(m)
            if mid is not None:
                cnt[mid] += 1
                tmin = t if tmin is None else min(tmin, t)
                tmax = t if tmax is None else max(tmax, t)
    print(f'\n=== {name} (t={tmin:.1f}~{tmax:.1f}) ===')
    for k, v in sorted(cnt.items(), key=lambda x: -x[1]):
        if isinstance(k, int):
            print(f'  M{k:5d} x{v}')
        else:
            print(f'  {k:6s} x{v}')
