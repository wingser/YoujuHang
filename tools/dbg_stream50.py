# -*- coding: utf-8 -*-
import subprocess, struct, sys
sys.stdout.reconfigure(encoding='utf-8')
TSHARK = r'D:\Program Files\Wireshark\tshark.exe'
PCAP = r'D:\Git\YoujuHang\captures\team_20260827111730_00001_20260827111732.pcapng'

def parse_h2(segments):
    buf = b''
    frames = []
    for t, d in segments:
        buf += d
        while True:
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

cmd = [TSHARK, '-r', PCAP, '-Y', 'tcp.stream==50 && tcp.len>0',
       '-T', 'fields', '-e', 'frame.time_relative', '-e', 'ip.src', '-e', 'tcp.len', '-e', 'tcp.payload']
out = subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8', errors='replace').stdout
streams = {}
for line in out.splitlines():
    p = line.strip().split('\t')
    if len(p) < 4 or not p[3]: continue
    t, src, ln, hd = p[0], p[1], p[2], p[3]
    streams.setdefault(src, []).append((float(t), bytes.fromhex(hd)))
print('directions:', list(streams.keys()))
for src, segs in streams.items():
    fl = parse_h2(segs)
    print(f'  {src}: {len(fl)} frames')
    reqdata = [f for f in fl if f[1] == 0]
    print(f'  DATA frames: {len(reqdata)}, first sid:', reqdata[0][3] if reqdata else None)
    for f in fl[:5]:
        print('    t=%.3f type=%d flags=%d sid=%d len=%d' % (f[0], f[1], f[2], f[3], len(f[4])))
