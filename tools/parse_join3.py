# -*- coding: utf-8 -*-
"""解析 join3 抓包：18141 房间二进制协议 + 18180 大厅协议。
帧格式（C->S/S->C 18141）：[00 00 XX YY][TT 00 00 07 CC] + 数据，XX 长度或类型。
"""
import base64, struct, subprocess, sys, re
sys.stdout.reconfigure(encoding='utf-8', errors='replace')

TSHARK = r'D:\Program Files\Wireshark\tshark.exe'
# 去掉脚本参数里的选项，仅保留首个非 '-' 参数作为 pcap
_argv = [a for a in sys.argv[1:] if not a.startswith('--')]
pcap = _argv[0] if _argv else r'D:\Git\YoujuHang\captures\join3_20260827_00001_20260827152918.pcapng'

def q(fields, filt):
    cmd = [TSHARK, '-r', pcap, '-Y', filt, '-T', 'fields'] + fields
    return subprocess.run(cmd, capture_output=True, text=True, encoding='utf-8', errors='replace').stdout

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
            else:
                lines.append(f'{pad}f{field} wire={wire} @{i}'); break
    except Exception as e:
        lines.append(f'{pad}!! {e}')
    return lines

def dump_port(port, title, show_pb=True, maxlines=30):
    print('\n' + '=' * 76)
    print(f'=== {title} (port {port}) ===')
    for line in q(['-e','frame.number','-e','frame.time_relative','-e','ip.src','-e','tcp.srcport','-e','tcp.len','-e','tcp.payload'], f'tcp.port=={port} && tcp.len>0').splitlines():
        parts = line.strip().split('\t')
        if len(parts) != 6 or not parts[5]: continue
        fno, t, src, sp, ln, hexd = parts
        raw = bytes.fromhex(hexd)
        if len(raw) < 8: continue
        direction = 'C->S' if src.startswith('192.') else 'S->C'
        # 尝试帧头 [00 00 XX YY]
        head = raw[:4].hex()
        body = raw[4:]
        print(f'#{fno} [{t}] {direction} len={len(raw)} head={head} rest={body[:20].hex()}')
        if show_pb and len(body) > 5 and body[1:3] == b'\x00\x00':
            # 数据区尝试 protobuf
            pb = body[5:] if body[0] == 0x01 else body
            # 若 pb 里出现 0x91 09 前缀则跳过
            print('     PB:')
            for l in decode_pb(pb, 1)[:12]:
                print('       ', l)

def dump_stream_18180():
    print('\n' + '=' * 76)
    print('=== 18180 大厅协议（91 09 00 08 推送/心跳） ===')
    for line in q(['-e','frame.number','-e','frame.time_relative','-e','ip.src','-e','tcp.len','-e','tcp.payload'], 'tcp.port==18180 && tcp.len>0').splitlines():
        parts = line.strip().split('\t')
        if len(parts) != 5 or not parts[4]: continue
        fno, t, src, ln, hexd = parts
        raw = bytes.fromhex(hexd)
        direction = 'C->S' if src.startswith('192.') else 'S->C'
        # 心跳 00 00 00 00 25 02 00
        if len(raw) >= 7 and raw[:4] == b'\x00\x00\x00\x00':
            print(f'#{fno} [{t}] {direction} HEARTBEAT {raw.hex()}')
            continue
        # 推送 2d000000 91090008
        if len(raw) >= 4 and raw[:4] == b'\x2d\x00\x00\x00':
            body = raw[8:]
            print(f'#{fno} [{t}] {direction} PUSH len={len(raw)} body={body.hex()[:80]}')
            for l in decode_pb(body, 1)[:14]:
                print('     ', l)
            continue
        print(f'#{fno} [{t}] {direction} OTHER {raw.hex()[:80]}')

if __name__ == '__main__':
    if '--stream' in sys.argv:
        dump_stream_18180()
    elif '--18141' in sys.argv:
        dump_port(18141, '18141 房间协议', show_pb=False)
    elif '--18141pb' in sys.argv:
        dump_port(18141, '18141 房间协议', show_pb=True)
    else:
        dump_port(18141, '18141 房间协议', show_pb=False)
        dump_stream_18180()
