# -*- coding: utf-8 -*-
"""解析 join3 抓包 18141 帧：提取所有消息，标记时间线。
帧格式: [00 00 XX YY] [TT 00 00 07 CC] + 数据(XX字节)
  YY=01 -> 明文二进制 (TT=04)；YY=00 -> base64 (TT=01)
"""
import base64, subprocess, sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

TSHARK = r'D:\Program Files\Wireshark\tshark.exe'
_argv = [a for a in sys.argv[1:] if not a.startswith('--')]
pcap = _argv[0] if _argv else r'D:\Git\YoujuHang\captures\join3_20260827_00001_20260827152918.pcapng'

def q(fields, filt):
    cmd = [TSHARK, '-r', pcap, '-Y', filt, '-T', 'fields'] + fields
    return subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8', errors='replace').stdout

# 逐帧解析 18141，按 (tcp.len>0) 重组（简单模式：假设每帧一个消息，允许拆分标记）
rows = []
for line in q(['-e','frame.number','-e','frame.time_relative','-e','ip.src','-e','tcp.len','-e','tcp.payload'], 'tcp.port==18141 and tcp.len>0').splitlines():
    p = line.strip().split('\t')
    if len(p) != 5 or not p[4]: continue
    fno, t, src, ln, hexd = p
    raw = bytes.fromhex(hexd)
    direction = 'C->S' if src.startswith('192.') else 'S->C'
    rows.append((float(t), fno, direction, raw))

# 组装消息流：按 TCP 顺序（同一方向内帧头 00 00 可能拆分）
# 简化：直接按帧解析（此抓包每个 TCP 段似乎正好一个/多个完整消息）
out = []
for t, fno, direction, raw in rows:
    i = 0
    while i + 4 <= len(raw):
        if raw[i:i+2] != b'\x00\x00':
            # 不够一个帧头，跳过剩余
            break
        xlen = raw[i+2]
        flag = raw[i+3]
        if i + 4 + 5 > len(raw):
            break
        tt = raw[i+4]
        cseq = raw[i+5:i+8].hex()  # 00 07 XX
        data = raw[i+9:i+9+xlen]
        if len(data) != xlen:
            break
        out.append((t, fno, direction, flag, cseq, data, i))
        i += 9 + xlen
    if i < len(raw):
        out.append((t, fno, direction, 'PARTIAL', '', raw[i:], i))

print(f'总帧数: {len(rows)}, 解析消息数: {len(out)}')
print('=' * 120)
for t, fno, direction, flag, cseq, data, pos in out:
    kind = 'PLAIN' if flag == '01' else ('B64' if flag == '00' else f'FLAG{flag}')
    if flag == '01':
        # 明文：心跳数据 cd83c786... / 响应 88e4d9be
        print(f'#{fno} t={t:8.3f} {direction} [{kind}] cseq={cseq} hex={data.hex()}')
    else:
        try:
            b = base64.b64decode(data + b'=' * (-len(data) % 4))
        except Exception:
            b = b''
        print(f'#{fno} t={t:8.3f} {direction} [{kind}] cseq={cseq} b64len={len(data)} -> {b.hex()[:120]}')
