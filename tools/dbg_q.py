# -*- coding: utf-8 -*-
import sys, subprocess
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
TSHARK = r'D:\Program Files\Wireshark\tshark.exe'
pcap = r'D:\Git\YoujuHang\captures\join3_20260827_00001_20260827152918.pcapng'
filt = 'tcp.port==18141 and tcp.len>0'
fields = ['-e','frame.number','-e','frame.time_relative','-e','ip.src','-e','tcp.srcport','-e','tcp.len','-e','tcp.payload']
cmd = [TSHARK, '-r', pcap, '-Y', filt, '-T', 'fields'] + fields
out = subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8', errors='replace')
print('RC', out.returncode, 'OUTLEN', len(out.stdout))
print(out.stdout[:200])
print('ERR', out.stderr[:300])
