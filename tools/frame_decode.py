# -*- coding: utf-8 -*-
"""封帧解析器：HTTP/2 帧 + protobuf + base64 检测

用法:
  python tools/frame_decode.py flow <pcap> <ip>            # 逐包解析该 IP 的流
  python tools/frame_decode.py frames <bin_file>            # 解析二进制文件中的 HTTP/2 帧
  python tools/frame_decode.py pb <hex_or_b64>              # 解析 protobuf
"""
import base64
import glob
import os
import re
import subprocess
import sys

TSHARK = r"D:\Program Files\Wireshark\tshark.exe"

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
    sys.stderr.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


# ---------- protobuf 解析 ----------

def parse_varint(data, pos):
    result = 0
    shift = 0
    while pos < len(data):
        b = data[pos]
        pos += 1
        result |= (b & 0x7F) << shift
        if not (b & 0x80):
            break
        shift += 7
    return result, pos


def parse_pb(data, indent=0, max_depth=6):
    """解析 protobuf 字节，返回 (字段描述列表, 尾随字节)"""
    out = []
    pos = 0
    pad = "  " * indent
    while pos < len(data):
        start = pos
        try:
            tag, pos = parse_varint(data, pos)
        except Exception:
            out.append("%s[tag parse fail at %d]" % (pad, start))
            break
        field = tag >> 3
        wire = tag & 7
        if wire == 0:  # varint
            try:
                val, pos = parse_varint(data, pos)
            except Exception:
                out.append("%s%d: varint (truncated)" % (pad, field))
                break
            out.append("%s%d: varint = %d (0x%x)" % (pad, field, val, val))
        elif wire == 1:  # fixed64
            if pos + 8 <= len(data):
                val = data[pos:pos + 8]
                pos += 8
                out.append("%s%d: fixed64 = %s" % (pad, field, val.hex()))
            else:
                out.append("%s%d: fixed64 (truncated)" % (pad, field))
                break
        elif wire == 2:  # length-delimited
            try:
                ln, pos = parse_varint(data, pos)
            except Exception:
                out.append("%s%d: bytes (truncated len)" % (pad, field))
                break
            if pos + ln > len(data):
                out.append("%s%d: bytes len=%d (truncated)" % (pad, field, ln))
                break
            chunk = data[pos:pos + ln]
            pos += ln
            if indent < max_depth and looks_like_pb(chunk):
                out.append("%s%d: message (len=%d):" % (pad, field, ln))
                out.extend(parse_pb(chunk, indent + 1, max_depth))
            else:
                out.append("%s%d: bytes(len=%d): %s" % (
                    pad, field, ln,
                    repr(chunk) if all(32 <= b < 127 for b in chunk)
                    else chunk.hex()))
        elif wire == 5:  # fixed32
            if pos + 4 <= len(data):
                val = data[pos:pos + 4]
                pos += 4
                out.append("%s%d: fixed32 = %s" % (pad, field, val.hex()))
            else:
                out.append("%s%d: fixed32 (truncated)" % (pad, field))
                break
        else:
            out.append("%s%d: wire=%d (unsupported)" % (pad, field, wire))
            break
    tail = data[pos:] if pos < len(data) else b""
    return out, tail


def flatten(items):
    """把 parse_pb 返回的嵌套列表扁平化为字符串行"""
    out = []
    for it in items:
        if isinstance(it, list):
            out.extend(flatten(it))
        else:
            out.append(str(it))
    return out


def looks_like_pb(data):
    """启发式判断是否像 protobuf message"""
    if len(data) < 2:
        return False
    try:
        tag, _ = parse_varint(data, 0)
    except Exception:
        return False
    if tag == 0 or (tag >> 3) == 0 or (tag & 7) > 5:
        return False
    return True


# ---------- HTTP/2 帧解析 ----------

H2_TYPES = {
    0x0: "DATA", 0x1: "HEADERS", 0x2: "PRIORITY", 0x3: "RST_STREAM",
    0x4: "SETTINGS", 0x5: "PUSH_PROMISE", 0x6: "PING", 0x7: "GOAWAY",
    0x8: "WINDOW_UPDATE", 0x9: "CONTINUATION",
}


def parse_h2_frames(data):
    """解析 HTTP/2 帧序列，返回帧列表"""
    frames = []
    pos = 0
    if data.startswith(b"PRI * HTTP/2.0"):
        # HTTP/2 客户端连接前言 24 字节
        frames.append({"off": 0, "len": 24, "type": "PREAMBLE",
                       "flags": 0, "stream": 0,
                       "payload": data[:24]})
        pos = 24
    while pos + 9 <= len(data):
        length = (data[pos] << 16) | (data[pos + 1] << 8) | data[pos + 2]
        ftype = data[pos + 3]
        flags = data[pos + 4]
        stream = int.from_bytes(data[pos + 5:pos + 9], "big")
        if pos + 9 + length > len(data):
            frames.append({"partial": True, "off": pos,
                           "len": length, "type": ftype,
                           "flags": flags, "stream": stream,
                           "payload": data[pos + 9:]})
            break
        payload = data[pos + 9:pos + 9 + length]
        frames.append({"off": pos, "len": length, "type": ftype,
                       "flags": flags, "stream": stream, "payload": payload})
        pos += 9 + length
    return frames


def fmt_h2_frame(fr):
    if fr.get("type") == "PREAMBLE":
        return "off=%-6d %-9s len=%-4d %s" % (
            fr["off"], "PREAMBLE", fr["len"], repr(fr["payload"][:24]))
    tname = H2_TYPES.get(fr["type"], "T%02X" % fr["type"])
    extra = ""
    if fr["type"] == 0x0 and fr["len"] > 0:  # DATA
        extra = try_b64(fr["payload"])
    elif fr["type"] == 0x1 and fr["len"] > 0:  # HEADERS
        extra = "hex:" + fr["payload"][:48].hex()
    return "off=%-6d %-9s len=%-4d flags=0x%02x stream=%d  %s" % (
        fr["off"], tname, fr["len"], fr["flags"], fr["stream"], extra)


def try_b64(data):
    """尝试把 payload 解码为 base64(protobuf)，失败返回 hex"""
    try:
        s = data.decode("ascii")
    except UnicodeDecodeError:
        return "hex:" + data[:64].hex()
    if re.fullmatch(r"[A-Za-z0-9+/=]+", s) and len(s) >= 8 and len(s) % 4 == 0:
        try:
            raw = base64.b64decode(s)
            lines, tail = parse_pb(raw)
            if not tail:
                return "b64->pb:\n" + "\n".join(flatten(lines))
            return "b64(hex %d): %s" % (len(raw), raw[:64].hex())
        except Exception:
            return "b64:%s" % s[:64]
    return repr(s[:64])


# ---------- 命令 ----------

def cmd_frames(path):
    data = open(path, "rb").read()
    frames = parse_h2_frames(data)
    for fr in frames:
        print(fmt_h2_frame(fr))
    if frames and frames[-1].get("partial"):
        print("... partial frame at end (%d bytes, need %d)" % (
            len(frames[-1]["payload"]), frames[-1]["len"]))
    print("total frames:", len(frames))


def cmd_flow(pcap, ip):
    """用 tshark 提取该 IP 的每包载荷并解析为 HTTP/2 帧"""
    text = subprocess.run([TSHARK, "-r", pcap, "-Y", "tcp.payload",
                           "-T", "fields", "-e", "frame.time_relative",
                           "-e", "ip.src", "-e", "tcp.srcport",
                           "-e", "ip.dst", "-e", "tcp.dstport",
                           "-e", "tcp.payload"],
                          capture_output=True).stdout.decode("utf-8", "replace")
    # 按流分组
    streams = {}
    order = []
    for ln in text.splitlines():
        parts = ln.split("\t")
        if len(parts) < 6:
            continue
        src, dst = parts[1], parts[3]
        if ip not in (src, dst):
            continue
        key = (src, parts[2], dst, parts[4])
        if key not in streams:
            streams[key] = []
            order.append(key)
        streams[key].append((parts[0], parts[5]))
    for key in order:
        print("=" * 78)
        print("flow %s:%s -> %s:%s" % key)
        buf = b""
        for t, hexs in streams[key]:
            buf += bytes.fromhex(hexs)
            frames = parse_h2_frames(buf)
            if not frames:
                print("t=%s (buffer %d bytes, waiting frames)" % (t, len(buf)))
                continue
            # 打印完整的帧
            consumed = 0
            for fr in frames:
                end = fr["off"] + 9 + fr.get("len", 0)
                consumed = max(consumed, end)
                print("t=%-8s %s" % (t, fmt_h2_frame(fr)))
            buf = buf[consumed:]


def cmd_pb(hex_or_b64):
    if re.fullmatch(r"[A-Za-z0-9+/=]+", hex_or_b64) and len(hex_or_b64) % 4 == 0 \
            and not re.fullmatch(r"[0-9a-fA-F]{2,}", hex_or_b64):
        data = base64.b64decode(hex_or_b64)
    elif re.fullmatch(r"[0-9a-fA-F]+", hex_or_b64):
        data = bytes.fromhex(hex_or_b64)
    else:
        print("input should be hex or base64")
        return
    lines, tail = parse_pb(data)
    print("decoded %d bytes:" % len(data))
    print("\n".join(flatten(lines)))
    if tail:
        print("TAIL (%d bytes): %s" % (len(tail), tail.hex()))


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(1)
    cmd = sys.argv[1]
    if cmd == "frames":
        cmd_frames(sys.argv[2])
    elif cmd == "flow":
        cmd_flow(sys.argv[2], sys.argv[3])
    elif cmd == "pb":
        cmd_pb(sys.argv[2])
    else:
        print("unknown cmd:", cmd)
