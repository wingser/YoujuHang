# -*- coding: utf-8 -*-
"""X-Zone.exe 静态分析：PE 结构 + 字符串 xref + 反汇编

用法:
  python tools/pe_static.py info                       # PE 基本信息
  python tools/pe_static.py xref <rva_hex> [context]  # 搜索引用某 RVA 的指令
  python tools/pe_static.py disasm <rva_hex> <len>    # 反汇编某 RVA 处 N 字节
  python tools/pe_static.py strref <substr>           # 按字符串内容反查 xref
"""
import glob
import os
import re
import struct
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def find_exe():
    m = glob.glob(r"D:/Game/**/bin/X-Zone.exe", recursive=True)
    return m[0] if m else None


def load_pe(path):
    data = open(path, "rb").read()
    pe = struct.unpack_from("<I", data, 0x3C)[0]
    magic = struct.unpack_from("<H", data, pe + 24)[0]  # OptionalHeader magic
    is64 = (magic == 0x20B)
    nsec = struct.unpack_from("<H", data, pe + 6)[0]
    size_opt = struct.unpack_from("<H", data, pe + 20)[0]  # SizeOfOptionalHeader
    if is64:
        image_base = struct.unpack_from("<Q", data, pe + 24 + 24)[0]
    else:
        image_base = struct.unpack_from("<I", data, pe + 24 + 28)[0]
    sec_off = pe + 24 + size_opt
    sections = []
    for i in range(nsec):
        off = sec_off + i * 40
        name = data[off:off + 8].rstrip(b"\x00").decode("ascii", "replace")
        vsize = struct.unpack_from("<I", data, off + 8)[0]
        vaddr = struct.unpack_from("<I", data, off + 12)[0]
        rsize = struct.unpack_from("<I", data, off + 16)[0]
        roff = struct.unpack_from("<I", data, off + 20)[0]
        sections.append({"name": name, "vaddr": vaddr, "vsize": vsize,
                         "roff": roff, "rsize": rsize})
    return data, {"is64": is64, "image_base": image_base,
                  "sections": sections, "pe_off": pe}


def rva_to_off(pe, rva):
    for s in pe["sections"]:
        if s["vaddr"] <= rva < s["vaddr"] + max(s["vsize"], s["rsize"]):
            return s["roff"] + (rva - s["vaddr"])
    return None


def off_to_rva(pe, off):
    for s in pe["sections"]:
        if s["roff"] <= off < s["roff"] + s["rsize"]:
            return s["vaddr"] + (off - s["roff"])
    return None


def get_text_range(pe):
    for s in pe["sections"]:
        if s["name"] == ".text":
            return s["roff"], s["roff"] + s["rsize"]
    return None, None


def cmd_info(path):
    data, pe = load_pe(path)
    print("is64:", pe["is64"])
    print("image_base: 0x%X" % pe["image_base"])
    for s in pe["sections"]:
        print("  %-8s vaddr=0x%08X vsize=0x%08X roff=0x%08X rsize=0x%08X"
              % (s["name"], s["vaddr"], s["vsize"], s["roff"], s["rsize"]))


def cmd_xref(path, rva_hex, context=0):
    """搜索 .text 中对指定 RVA 的引用"""
    data, pe = load_pe(path)
    rva = int(rva_hex, 16)
    toff, tend = get_text_range(pe)
    text = data[toff:tend]
    # 匹配模式：push imm32 / mov r32,imm32 / lea r32,[rip+disp32] / mov r32,[rip+disp32]
    patterns = []
    # 32位: push imm32 (68 xx xx xx xx), mov reg,imm32 (B8+rd xx xx xx xx)
    # lea r32, [rip+disp32] (48 8D xx disp32)
    rip_addr = pe["image_base"] + (toff - pe["sections"][0]["roff"] + 0)  # 起始 VA
    hits = []
    pos = 0
    n = len(text)
    while pos < n - 4:
        b = text[pos]
        # push imm32
        if b == 0x68:
            imm = struct.unpack_from("<I", text, pos + 1)[0]
            if imm == rva:
                hits.append(("push imm32", pos))
                pos += 5
                continue
        # mov reg, imm32 (B8-BF)
        if 0xB8 <= b <= 0xBF:
            imm = struct.unpack_from("<I", text, pos + 1)[0]
            if imm == rva:
                hits.append(("mov r%d,imm32" % (b - 0xB8), pos))
                pos += 5
                continue
        # 64位: lea r32,[rip+disp32] / mov r32,[rip+disp32] (48 8D/48 8B xx disp32)
        if b == 0x48 and pos + 6 < n and text[pos + 1] in (0x8D, 0x8B, 0x89, 0x8F, 0x8B + 1):
            # 计算目标：下条指令地址 + disp32
            if pos + 6 <= n:
                disp = struct.unpack_from("<i", text, pos + 3)[0]
                insn_va = pe["image_base"] + off_to_rva(pe, toff + pos)
                target = insn_va + 7 + disp
                target_rva = target - pe["image_base"]
                if target_rva & 0xFFFFFFFF == rva:
                    hits.append(("lea/mov rip-rel", pos))
        pos += 1
    if not hits:
        print("no direct xref found for RVA 0x%X" % rva)
        return
    for kind, pos in hits:
        va = pe["image_base"] + off_to_rva(pe, toff + pos)
        print("xref at file_off=0x%X rva=0x%X (%s)" % (toff + pos, va - pe["image_base"], kind))
        if context:
            st = max(0, pos - context)
            en = min(len(text), pos + context + 5)
            print("  bytes:", text[st:en].hex())


def cmd_strref(path, substr):
    """按字符串内容查 RVA，再找 xref"""
    data, pe = load_pe(path)
    pat = substr.encode()
    hits = []
    for s in pe["sections"]:
        if s["name"] in (".rdata", ".data", ".data1"):
            chunk = data[s["roff"]:s["roff"] + s["rsize"]]
            start = 0
            while True:
                i = chunk.find(pat, start)
                if i < 0:
                    break
                rva = s["vaddr"] + i
                hits.append(rva)
                start = i + 1
    if not hits:
        print("string not found:", substr)
        return
    for rva in hits[:20]:
        print("string RVA 0x%X" % rva)
        cmd_xref(path, "0x%X" % rva, 0)


def cmd_disasm(path, rva_hex, length):
    from capstone import Cs, CS_ARCH_X86, CS_MODE_32, CS_MODE_64
    data, pe = load_pe(path)
    rva = int(rva_hex, 16)
    off = rva_to_off(pe, rva)
    if off is None:
        print("rva out of range")
        return
    code = data[off:off + int(length, 16)]
    mode = CS_MODE_64 if pe["is64"] else CS_MODE_32
    md = Cs(CS_ARCH_X86, mode)
    md.detail = True
    va = pe["image_base"] + rva
    for insn in md.disasm(code, va):
        print("0x%08X  %-24s %s %s" % (insn.address, insn.bytes.hex(), insn.mnemonic, insn.op_str))


if __name__ == "__main__":
    if len(sys.argv) < 2:
        print(__doc__)
        sys.exit(1)
    path = find_exe() or sys.argv[2] if len(sys.argv) > 2 else find_exe()
    # 修正参数：第一个参数是命令
    cmd = sys.argv[1]
    args = sys.argv[2:]
    if not os.path.exists(args[0]) if args else True:
        pass
    # path 使用自动探测
    if cmd == "info":
        cmd_info(find_exe())
    elif cmd == "xref":
        cmd_xref(find_exe(), args[0], int(args[1]) if len(args) > 1 else 0)
    elif cmd == "disasm":
        cmd_disasm(find_exe(), args[0], args[1])
    elif cmd == "strref":
        cmd_strref(find_exe(), args[0])
    else:
        print("unknown cmd:", cmd)
