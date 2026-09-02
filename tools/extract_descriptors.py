#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""从游聚 X-Zone.exe 中提取嵌入的 protobuf 消息定义（DescriptorProto wire format），
输出为可读的 .proto 文本。

用法:
    python extract_descriptors.py <pe_path> [--out proto/all.proto]
"""
import argparse
import glob
import re
import sys
from pathlib import Path

from google.protobuf import descriptor_pb2

NAME_RE = re.compile(r"^[A-Za-z_][A-Za-z0-9_]*$")

TYPE_NAMES = {
    1: "double", 2: "float", 3: "int64", 4: "uint64",
    5: "int32", 6: "fixed64", 7: "fixed32", 8: "bool",
    9: "string", 10: "group", 11: "message", 12: "bytes",
    13: "uint32", 14: "enum", 15: "sfixed32", 16: "sfixed64",
    17: "sint32", 18: "sint64",
}
LABEL_NAMES = {1: "", 2: "required ", 3: "repeated "}


def read_varint(data, pos):
    result = 0
    shift = 0
    while True:
        if pos >= len(data):
            raise ValueError("truncated varint")
        b = data[pos]
        pos += 1
        result |= (b & 0x7F) << shift
        if not (b & 0x80):
            return result, pos
        shift += 7
        if shift > 70:
            raise ValueError("varint too long")


def try_parse_descriptor(data, tag_byte):
    """扫描所有 tag_byte 前缀的 length-delimited 字段，尝试按 DescriptorProto 解析。"""
    found = {}
    idx = 0
    while True:
        idx = data.find(bytes([tag_byte]), idx)
        if idx < 0:
            break
        try:
            ln, pos = read_varint(data, idx + 1)
        except ValueError:
            idx += 1
            continue
        if not (0 < ln <= 0x40000):
            idx += 1
            continue
        payload = data[pos:pos + ln]
        if len(payload) < ln:
            idx += 1
            continue
        dp = descriptor_pb2.DescriptorProto()
        try:
            dp.ParseFromString(payload)
        except Exception:
            idx += 1
            continue
        if not dp.name or not NAME_RE.match(dp.name):
            idx += 1
            continue
        if not dp.field and not dp.nested_type and not dp.enum_type:
            idx += 1
            continue
        ok = True
        for f in dp.field:
            if not NAME_RE.match(f.name) or not (1 <= f.number <= 536870911):
                ok = False
                break
        if not ok:
            idx += 1
            continue
        found[dp.name] = dp
        idx += 1
    return found


def simple_type_name(dp, field):
    """把字段类型转成名字：引用类型去掉 .Proto. 前缀，枚举取 TYPE 消息名。"""
    if field.type == field.TYPE_MESSAGE or field.type == field.TYPE_ENUM:
        tn = field.type_name.lstrip(".")
        return tn
    return TYPE_NAMES.get(field.type, "?")


def enum_values(dp):
    """从名为 TYPE 的嵌套消息中恢复枚举值 {EnumStart/ID/Kind} 序列。"""
    for n in dp.nested_type:
        if n.name == "TYPE":
            vals = []
            for f in n.field:
                if f.name == "EnumStart":
                    vals.append(("EnumStart", f.number))
                elif f.name == "ID":
                    vals.append(("ID", f.number))
                elif f.name == "Kind":
                    vals.append(("Kind", f.number))
            return vals
    return []


def format_message(dp, indent=0):
    pad = "  " * indent
    out = []
    if indent == 0:
        out.append(f"message {dp.name} {{")
    else:
        out.append(f"{pad}message {dp.name} {{")
    for f in dp.field:
        tn = simple_type_name(dp, f)
        label = LABEL_NAMES.get(f.label, "")
        out.append(f"{pad}  {label}{tn} {f.name} = {f.number};")
    for n in dp.nested_type:
        if n.name == "TYPE":
            continue
        out.append(format_message(n, indent + 1))
    for e in dp.enum_type:
        out.append(f"{pad}  enum {e.name} {{")
        for v in e.value:
            out.append(f"{pad}    {v.name} = {v.number};")
        out.append(f"{pad}  }}")
    out.append(f"{pad}}}")
    return "\n".join(out)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("pe", type=str, help="PE 文件路径（支持 * 通配符）")
    ap.add_argument("--out", type=str, default="proto/all.proto", help="输出文件")
    args = ap.parse_args()

    if any(c in args.pe for c in "*?"):
        matches = glob.glob(args.pe)
        if not matches:
            print(f"[-] no file matched: {args.pe}", file=sys.stderr)
            sys.exit(1)
        args.pe = matches[0]

    data = Path(args.pe).read_bytes()
    print(f"[*] scanning {len(data)} bytes ...", file=sys.stderr)
    msgs = try_parse_descriptor(data, 0x22)  # DescriptorProto (message_type/nested_type)
    enums = try_parse_descriptor(data, 0x2A)  # EnumDescriptorProto
    print(f"[+] messages: {len(msgs)}, enums: {len(enums)}", file=sys.stderr)

    lines = ['syntax = "proto3";', "", 'package Proto;', ""]
    for name in sorted(msgs):
        dp = msgs[name]
        if name.startswith("Proto."):
            dp.name = name[len("Proto."):]
        lines.append(format_message(dp))
        lines.append("")
    for name in sorted(enums):
        e = enums[name]
        if name.startswith("Proto."):
            e.name = name[len("Proto."):]
        lines.append(f"enum {e.name} {{")
        for v in e.value:
            lines.append(f"  {v.name} = {v.number};")
        lines.append("}")
        lines.append("")

    out_path = Path(args.out)
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text("\n".join(lines), encoding="utf-8")
    print(f"[+] {len(msgs)} messages + {len(enums)} enums -> {out_path}", file=sys.stderr)


if __name__ == "__main__":
    main()
