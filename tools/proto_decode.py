# -*- coding: utf-8 -*-
"""解析 protobuf 二进制（递归），供协议分析使用"""
import sys

def _varint(data, pos):
    result = 0
    shift = 0
    while True:
        if pos >= len(data):
            raise ValueError("varint EOF")
        b = data[pos]
        result |= (b & 0x7F) << shift
        pos += 1
        if not (b & 0x80):
            break
        shift += 7
        if shift > 70:
            raise ValueError("varint too long")
    return result, pos

def fmt_bytes(b):
    # 尝试 utf-8 解码，否则 hex
    try:
        s = b.decode("utf-8")
        for ch in s:
            if ord(ch) < 0x20 and ch not in "\r\n\t":
                raise ValueError()
        return repr(s)
    except Exception:
        return "hex(%s)" % b.hex()

def parse_msg(data, depth=0, max_depth=8):
    """返回字段列表 [(tag, wire, key, value_str)]"""
    fields = []
    pos = 0
    while pos < len(data):
        try:
            tag, pos = _varint(data, pos)
        except Exception:
            fields.append((None, None, None, "TRUNC"))
            break
        field = tag >> 3
        wire = tag & 7
        if wire == 0:  # varint
            v, pos = _varint(data, pos)
            fields.append((field, wire, "V", v))
        elif wire == 1:  # 64-bit
            if pos + 8 > len(data):
                fields.append((field, wire, "TRUNC64", ""))
                break
            v = data[pos:pos+8].hex()
            pos += 8
            fields.append((field, wire, "F64", v))
        elif wire == 2:  # length-delimited
            ln, pos = _varint(data, pos)
            if pos + ln > len(data):
                fields.append((field, wire, "TRUNCLEN", ln))
                break
            sub = data[pos:pos+ln]
            pos += ln
            if depth < max_depth:
                subfields = parse_msg(sub, depth+1, max_depth) if _looks_msg(sub) else []
                if subfields:
                    fields.append((field, wire, "MSG", "{" + ", ".join("f%d%s=%s" % (f, w, v) for f, w, _, v in subfields) + "}"))
                else:
                    fields.append((field, wire, "S", fmt_bytes(sub)))
            else:
                fields.append((field, wire, "S", fmt_bytes(sub)))
        elif wire == 5:  # 32-bit
            if pos + 4 > len(data):
                fields.append((field, wire, "TRUNC32", ""))
                break
            v = data[pos:pos+4].hex()
            pos += 4
            fields.append((field, wire, "F32", v))
        else:
            fields.append((field, wire, "W%d" % wire, "?"))
            break
    return fields

def _looks_msg(b):
    if not b:
        return False
    # 检查是否是合法 protobuf：首字节是合法 tag（field>0, wire in 0,1,2,5）
    try:
        tag, _ = _varint(b, 0)
        w = tag & 7
        return w in (0, 1, 2, 5) and (tag >> 3) > 0
    except Exception:
        return False

def parse_msg_str(data):
    fields = parse_msg(data)
    parts = []
    for f, w, k, v in fields:
        parts.append("f%d%s=%s" % (f, w, v))
    return "{ " + ", ".join(parts) + " }"

if __name__ == "__main__":
    import sys
    if len(sys.argv) > 1:
        raw = open(sys.argv[1], "rb").read()
        print(parse_msg_str(raw))
