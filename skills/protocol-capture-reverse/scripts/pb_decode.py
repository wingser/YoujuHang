# -*- coding: utf-8 -*-
"""极简 protobuf 解码器（游聚协议逆向用）。

不依赖 protoc / .proto 文件，直接从字节流还原字段结构：
  - 递归解析嵌套子消息
  - 自动识别可打印 UTF-8 字符串
  - 支持 varint / 64bit / len-delimited / 32bit / group

命令行用法:
    python pb_decode.py <hex字符串>
    python pb_decode.py -f <二进制文件>
    echo "<hex>" | python pb_decode.py -

作为模块:
    from pb_decode import decode, dumps, Field
    fields = decode(data)
    print(dumps(fields))
"""
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


class Field(object):
    """一个解码后的 protobuf 字段"""

    __slots__ = ("num", "wire", "val", "raw", "children")

    def __init__(self, num, wire, val=None, raw=None, children=None):
        self.num = num      # 字段号
        self.wire = wire    # wire type 0/1/2/5
        self.val = val      # wire=0/1/5 时的整数值
        self.raw = raw      # wire=2 时的原始字节
        self.children = children  # wire=2 且判定为嵌套消息时的子字段列表

    @property
    def text(self):
        """若为可打印字符串则返回 str，否则 None"""
        if self.raw is None:
            return None
        try:
            s = self.raw.decode("utf-8")
        except Exception:
            return None
        if not s:
            return None
        for c in s:
            o = ord(c)
            if o == 10 or 32 <= o < 127 or o > 127:
                continue
            return None
        return s


def read_varint(b, i):
    result = 0
    shift = 0
    while True:
        if i >= len(b):
            raise ValueError("varint overrun")
        byte = b[i]
        i += 1
        result |= (byte & 0x7F) << shift
        if not (byte & 0x80):
            break
        shift += 7
        if shift > 63:
            raise ValueError("varint too long")
    return result, i


def _looks_like_message(data):
    """启发式：判断 wire=2 的载荷是否像嵌套消息。

    先排除明显是文本的情况，再尝试完整解析一遍——能无损解析完才认为是消息。
    """
    if not data or len(data) > 1 << 20:
        return False
    # 全是可打印 ASCII/UTF-8 且以可打印字符开头 → 当成字符串
    try:
        s = data.decode("utf-8")
        if s and all(ord(c) == 10 or 32 <= ord(c) < 127 or ord(c) > 127 for c in s):
            # 但纯数字常见于是 varint 编码的嵌套，短文本仍优先当字符串
            return False
    except Exception:
        pass
    i = 0
    n = len(data)
    try:
        while i < n:
            key, i = read_varint(data, i)
            wire = key & 7
            num = key >> 3
            if num == 0:
                return False
            if wire == 0:
                _, i = read_varint(data, i)
            elif wire == 1:
                i += 8
            elif wire == 2:
                ln, i = read_varint(data, i)
                i += ln
            elif wire == 5:
                i += 4
            else:
                return False
            if i > n:
                return False
        return i == n
    except Exception:
        return False


def decode(b, maxdepth=6, _depth=0):
    """把字节流解析为 Field 列表"""
    out = []
    i = 0
    n = len(b)
    while i < n:
        try:
            key, i = read_varint(b, i)
        except Exception:
            break
        num = key >> 3
        wire = key & 7
        if num == 0:
            break
        try:
            if wire == 0:
                v, i = read_varint(b, i)
                out.append(Field(num, wire, val=v))
            elif wire == 1:
                raw = b[i:i + 8]
                i += 8
                v = int.from_bytes(raw, "little")
                out.append(Field(num, wire, val=v, raw=raw))
            elif wire == 2:
                ln, i = read_varint(b, i)
                if i + ln > n:
                    break
                raw = b[i:i + ln]
                i += ln
                kids = None
                if _depth < maxdepth and _looks_like_message(raw):
                    kids = decode(raw, maxdepth, _depth + 1)
                out.append(Field(num, wire, raw=raw, children=kids))
            elif wire == 5:
                raw = b[i:i + 4]
                i += 4
                out.append(Field(num, wire, raw=raw))
            else:
                break  # wire=3/4 group 或未知，停止
        except Exception as e:
            out.append(Field(num, wire, raw=None))
            break
    return out


def dumps(fields, indent=0, show_hex=32):
    """把 Field 列表格式化为多行文本"""
    lines = []
    pad = "  " * indent
    for f in fields:
        t = f.text
        if f.children is not None:
            lines.append("%sf%d {" % (pad, f.num))
            lines.extend(dumps(f.children, indent + 1, show_hex))
            lines.append("%s}" % pad)
        elif f.wire == 2:
            if t is not None:
                lines.append('%sf%d = "%s"' % (pad, f.num, t))
            else:
                h = (f.raw or b"").hex()
                if len(h) > show_hex * 2:
                    h = h[:show_hex * 2] + "..."
                lines.append("%sf%d = HEX(%dB) %s" % (pad, f.num, len(f.raw or b""), h))
        elif f.wire == 0:
            # 同时给出有符号视角：服务器错误码常以 uint64 补码下发（如 -10）
            v = f.val
            sv = v - (1 << 64) if v >= (1 << 63) else v
            if sv < 0:
                lines.append("%sf%d = %d (i64=%d)" % (pad, f.num, v, sv))
            else:
                lines.append("%sf%d = %d" % (pad, f.num, v))
        else:
            lines.append("%sf%d wire=%d val=%s" % (pad, f.num, f.wire, f.val))
    return lines


def find(fields, num):
    """取第一个指定字段号的 Field，不存在返回 None"""
    for f in fields:
        if f.num == num:
            return f
    return None


def find_all(fields, num):
    return [f for f in fields if f.num == num]


def get_int(fields, num, default=0):
    f = find(fields, num)
    return f.val if (f is not None and f.val is not None) else default


def get_str(fields, num, default=""):
    f = find(fields, num)
    return f.text if (f is not None and f.text is not None) else default


def _main():
    args = [a for a in sys.argv[1:]]
    if not args:
        print(__doc__)
        return 0
    if args[0] in ("-f", "--file"):
        data = open(args[1], "rb").read()
    elif args[0] == "-":
        data = bytes.fromhex(sys.stdin.read().strip())
    else:
        data = bytes.fromhex(args[0].strip())
    depth = 6
    if "--depth" in args:
        depth = int(args[args.index("--depth") + 1])
    for line in dumps(decode(data, maxdepth=depth), show_hex=64):
        print(line)
    return 0


if __name__ == "__main__":
    sys.exit(_main())
