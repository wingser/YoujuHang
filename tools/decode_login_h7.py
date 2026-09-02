# -*- coding: utf-8 -*-
"""从 h7_xzone.log 提取登录请求 H2 DATA 帧并解码 LsLoginCMsg
用法: python tools/decode_login_h7.py
"""
import os
import re
import sys
import base64

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

LOG = os.path.join(os.environ.get("TEMP", r"C:\Windows\Temp"), "h7_xzone.log")


def parse_hex_lines(lines, start):
    """从 start 行开始读 hex 字节，直到遇到 [ts] 开头的新行"""
    out = bytearray()
    for ln in lines[start:]:
        if re.match(r"^\[", ln):
            break
        for b in ln.split():
            try:
                out.append(int(b, 16))
            except ValueError:
                pass
    return bytes(out)


def parse_varint(buf, off):
    v = 0
    shift = 0
    while True:
        b = buf[off]
        off += 1
        v |= (b & 0x7F) << shift
        if not (b & 0x80):
            break
        shift += 7
    return v, off


def proto_field(buf, off):
    """解析一个 field: 返回 (field_no, wire_type, value, new_off)"""
    tag, off = parse_varint(buf, off)
    fn = tag >> 3
    wt = tag & 7
    if wt == 0:  # varint
        v, off = parse_varint(buf, off)
        return fn, wt, v, off
    elif wt == 2:  # len-delimited
        ln, off = parse_varint(buf, off)
        v = buf[off:off + ln]
        off += ln
        return fn, wt, v, off
    elif wt == 1:  # fixed64
        return fn, wt, buf[off:off + 8], off + 8
    elif wt == 5:  # fixed32
        return fn, wt, buf[off:off + 4], off + 4
    else:
        return fn, wt, None, off


def dump_proto(buf, name, indent=0):
    off = 0
    while off < len(buf):
        try:
            fn, wt, v, off = proto_field(buf, off)
        except Exception as e:
            print("  " * indent + "... parse stop at %d: %s" % (off, e))
            break
        pad = "  " * indent
        if wt == 0:
            print("%sfield%d (varint) = %d (0x%x)" % (pad, fn, v, v))
        elif wt == 2:
            if len(v) <= 64 and all(32 <= b < 127 for b in v):
                print("%sfield%d (bytes) = b'%s'" % (pad, fn, v.decode("ascii", "replace")))
            else:
                print("%sfield%d (bytes) = %d bytes: %s" % (pad, fn, len(v), v.hex()))
                if len(v) > 4 and v[:4] != bytes.fromhex("00000000"):
                    dump_proto(v, name + ".f%d" % fn, indent + 1)
        elif wt == 1:
            print("%sfield%d (fixed64) = %s" % (pad, fn, v.hex()))
        elif wt == 5:
            print("%sfield%d (fixed32) = %s" % (pad, fn, v.hex()))
        else:
            print("%sfield%d (wire=%d) unknown" % (pad, fn, wt))
    print("")


def main():
    lines = open(LOG, encoding="utf-8", errors="replace").read().splitlines()
    # 找 sock=3152 的 WSASend DATA 帧（帧头 00 00 XX 00 01，type=0 DATA）
    i = 0
    found = 0
    while i < len(lines):
        m = re.match(r"^\[(\d+)\] SEND sock=(\d+) tag=ws2_32\.dll!WSASend bufCount=1 WSABUF\.len=(\d+)", lines[i])
        if m:
            i += 1
            data = parse_hex_lines(lines, i)
            i -= 1
            if len(data) >= 9 and data[3] == 0x00 and data[4] == 0x01:
                payload = data[9:]
                if len(payload) > 0:
                    print("=== H2 DATA frame sock=%s len=%d payload=%d bytes ===" % (m.group(2), len(data), len(payload)))
                    print("payload hex: %s" % payload.hex())
                    # 尝试 base64 解码（登录 body 是 base64）
                    try:
                        b64 = payload.decode("ascii").strip()
                        dec = base64.b64decode(b64)
                        print("--- base64 decode (%d bytes) ---" % len(dec))
                        print("hex: %s" % dec.hex())
                        if len(dec) >= 2 and dec[0] == 0x00 and dec[1] == 0x01:
                            print("!! [00 01] 标记确认，LsLoginCMsg 从 offset 2 开始:")
                            dump_proto(dec[2:], "LsLoginCMsg")
                    except Exception as e:
                        print("base64 fail: %s" % e)
                    found += 1
                    print("")
            i += 1
        else:
            i += 1
    print("total DATA frames: %d" % found)


if __name__ == "__main__":
    main()
