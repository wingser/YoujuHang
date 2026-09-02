# -*- coding: utf-8 -*-
"""分析 h5_xzone.log：提取 sock=4444 的 H2 帧，解码 base64/protobuf"""
import io
import re
import base64
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

LOG = r"C:\Users\Lenovo\AppData\Local\Temp\h5_xzone.log"

def hexs_to_bytes(hs):
    try:
        return bytes.fromhex(hs.replace(" ", "").replace("\n", ""))
    except Exception:
        return None

def parse_h2_frame(data):
    if len(data) < 9:
        return None
    length = (data[0] << 16) | (data[1] << 8) | data[2]
    ftype = data[3]
    flags = data[4]
    stream = (data[5] << 24) | (data[6] << 16) | (data[7] << 8) | data[8]
    body = data[9:9+length]
    return {"len": length, "type": ftype, "flags": flags, "stream": stream, "body": body}

FTYPES = {0: "DATA", 1: "HEADERS", 2: "PRIORITY", 3: "RST_STREAM", 4: "SETTINGS",
          5: "PUSH_PROMISE", 6: "PING", 7: "GOAWAY", 8: "WINDOW_UPDATE", 9: "CONTINUATION"}

def parse_varint(b, i):
    result = 0
    shift = 0
    while True:
        byte = b[i]
        i += 1
        result |= (byte & 0x7f) << shift
        if not (byte & 0x80):
            break
        shift += 7
    return result, i

def decode_protobuf(b):
    """简单 protobuf 解析"""
    fields = []
    i = 0
    try:
        while i < len(b):
            tag, i = parse_varint(b, i)
            field_num = tag >> 3
            wire = tag & 7
            if wire == 0:
                val, i = parse_varint(b, i)
                fields.append((field_num, "varint", val))
            elif wire == 2:
                length, i = parse_varint(b, i)
                val = b[i:i+length]
                i += length
                fields.append((field_num, "bytes", val))
            elif wire == 5:
                val = int.from_bytes(b[i:i+4], "little")
                i += 4
                fields.append((field_num, "fixed32", val))
            elif wire == 1:
                val = int.from_bytes(b[i:i+8], "little")
                i += 8
                fields.append((field_num, "fixed64", val))
            else:
                fields.append((field_num, "wire%d" % wire, b[i:]))
                break
    except Exception:
        pass
    return fields

def fmt_field(f):
    fn, wt, val = f
    if wt == "bytes":
        try:
            s = val.decode("ascii")
            if all(32 <= ord(c) < 127 for c in s):
                return "f%d=%r" % (fn, s)
        except Exception:
            pass
        return "f%d=<%s>" % (fn, val.hex())
    return "f%d=%s" % (fn, val)

def main():
    lines = io.open(LOG, encoding="utf-8", errors="replace").readlines()
    # 收集 sock=4444 的发送数据
    cur = {}
    socks = set()
    sends = []
    i = 0
    while i < len(lines):
        l = lines[i]
        m = re.search(r"\[(\d+)\] (SEND|RECV) sock=(\d+)", l)
        if m:
            t = int(m.group(1))
            dr = m.group(2)
            sock = int(m.group(3))
            socks.add(sock)
            # 下一行是数据
            data_lines = []
            j = i + 1
            while j < len(lines) and not lines[j].startswith("["):
                data_lines.append(lines[j])
                j += 1
            raw = "".join(data_lines)
            b = hexs_to_bytes(raw)
            if sock == 4444 and dr == "SEND" and b and len(b) >= 9:
                sends.append((t, b))
            i = j
        else:
            i += 1

    print("socks seen:", sorted(socks))
    print("sock=4444 SEND frames:", len(sends))
    for t, b in sends:
        fr = parse_h2_frame(b)
        if not fr:
            print(f"t={t} raw={b.hex()}")
            continue
        tname = FTYPES.get(fr["type"], "?%d" % fr["type"])
        print(f"t={t} {tname} len={fr['len']} flags={fr['flags']:02x} stream={fr['stream']}")
        body = fr["body"]
        if tname == "DATA":
            try:
                dec = base64.b64decode(body)
                print("  DATA b64 decoded (%d bytes): %s" % (len(dec), dec.hex()))
                fields = decode_protobuf(dec)
                print("  proto: " + ", ".join(fmt_field(f) for f in fields))
            except Exception as e:
                print("  DATA not b64: %s (%s)" % (body[:32].hex(), e))
        elif tname == "HEADERS":
            print("  HPACK block (%d bytes): %s" % (len(body), body.hex()))
        else:
            print("  body: %s" % body.hex())

if __name__ == "__main__":
    main()
