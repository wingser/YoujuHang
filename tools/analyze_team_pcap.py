# -*- coding: utf-8 -*-
"""解析战队/房间挂机抓包：18150 二进制通道 + 18151 HTTP/2 网关请求/响应。"""
import base64, struct, sys, subprocess, re
sys.stdout.reconfigure(encoding='utf-8')

def b64d(s):
    s = s.strip()
    return base64.urlsafe_b64decode(s + '=' * (-len(s) % 4))

def parse_varint(b, i):
    result, shift = 0, 0
    while True:
        if i >= len(b): raise ValueError('varint overrun')
        byte = b[i]; i += 1
        result |= (byte & 0x7F) << shift
        if not (byte & 0x80): break
        shift += 7
    return result, i

def decode_pb(b, indent=0, maxdepth=6):
    lines, i = [], 0
    pad = '  ' * indent
    try:
        while i < len(b):
            key, i = parse_varint(b, i)
            field, wire = key >> 3, key & 7
            if wire == 0:
                val, i = parse_varint(b, i)
                lines.append(f'{pad}f{field} = {val}')
            elif wire == 1:
                if i + 8 > len(b): break
                val = struct.unpack('<Q', b[i:i+8])[0]; i += 8
                lines.append(f'{pad}f{field} = 0x{val:016x} (fixed64)')
            elif wire == 2:
                ln, i = parse_varint(b, i)
                if i + ln > len(b): break
                data = b[i:i+ln]; i += ln
                try:
                    s = data.decode('utf-8')
                    if all(ord(c) == 10 or 32 <= ord(c) < 127 or ord(c) > 127 for c in s):
                        lines.append(f'{pad}f{field} = "{s}"')
                        continue
                except Exception:
                    pass
                try:
                    if indent < maxdepth:
                        sub = decode_pb(data, indent+1, maxdepth)
                        lines.append(f'{pad}f{field} = {{')
                        lines.extend(sub)
                        lines.append(f'{pad}}}')
                    else:
                        raise ValueError
                except Exception:
                    lines.append(f'{pad}f{field} = HEX {data[:48].hex()}')
            elif wire == 5:
                if i + 4 > len(b): break
                val = struct.unpack('<I', b[i:i+4])[0]; i += 4
                lines.append(f'{pad}f{field} = 0x{val:08x} (fixed32)')
            else:
                lines.append(f'{pad}f{field} wire={wire} @{i}'); break
    except Exception as e:
        lines.append(f'{pad}!! {e}')
    return lines

TSHARK = r'D:\Program Files\Wireshark\tshark.exe'

def q(fields, filt):
    cmd = [TSHARK, '-r', sys.argv[1] if len(sys.argv) > 1 else r'D:\Git\YoujuHang\captures\team_20260827111730_00001_20260827111732.pcapng',
           '-Y', filt, '-T', 'fields'] + fields
    return subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8', errors='replace').stdout

def section(title):
    print('\n' + '=' * 72 + '\n' + title + '\n' + '=' * 72)

def main():
    pcap = sys.argv[1] if len(sys.argv) > 1 else r'D:\Git\YoujuHang\captures\team_20260827111730_00001_20260827111732.pcapng'
    section(f'18150 二进制通道（{pcap}）')
    for line in q(['-e','frame.time_relative','-e','ip.src','-e','tcp.srcport','-e','tcp.payload'], 'tcp.port==18150 && tcp.len>0').splitlines():
        parts = line.strip().split('\t')
        if len(parts) != 4 or not parts[3]: continue
        t, src, port, hexd = parts
        raw = bytes.fromhex(hexd)
        if len(raw) < 7: continue
        ln = struct.unpack('<I', raw[:4])[0]
        mid = struct.unpack('<H', raw[4:6])[0]
        pb = raw[7:]
        if mid in (549, 550): continue
        direction = 'C->S' if src.startswith('192.') else 'S->C'
        print(f'[{t}] {direction} msgid={mid} len={ln}')
        for l in decode_pb(pb)[:14]:
            print('   ', l)

if __name__ == '__main__':
    main()
