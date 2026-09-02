#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""反汇编 X-Zone.exe（32 位 PE32），定位对 Winsock 导入
（send/recv/WSARecv/WSASend/connect/WSASendTo 等）的调用点，
输出调用点地址与其所在的"函数体"（以常见 prologue 为界粗分）。

用法: python find_sock_calls.py <pe_path> [--out out.txt]
"""
import argparse
import glob
import sys
from pathlib import Path

import pefile
from capstone import Cs, CS_ARCH_X86, CS_MODE_32

FUNCS = {
    "send", "recv", "WSARecv", "WSASend", "WSASendTo", "WSARecvFrom",
    "connect", "WSAConnect", "closesocket", "shutdown", "select",
    "GetQueuedCompletionStatus",
}


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("pe", type=str)
    ap.add_argument("--out", type=str, default="docs/sock_call_sites.txt")
    args = ap.parse_args()

    if any(c in args.pe for c in "*?"):
        m = glob.glob(args.pe)
        if not m:
            sys.exit("no file")
        args.pe = m[0]

    pe = pefile.PE(args.pe, fast_load=True)
    pe.parse_data_directories(directories=[
        pefile.DIRECTORY_ENTRY["IMAGE_DIRECTORY_ENTRY_IMPORT"],
    ])
    image_base = pe.OPTIONAL_HEADER.ImageBase

    # 收集目标函数 -> IAT 槽 VA 列表
    targets = {}
    if hasattr(pe, "DIRECTORY_ENTRY_IMPORT"):
        for entry in pe.DIRECTORY_ENTRY_IMPORT:
            for imp in entry.imports:
                if imp.name and imp.name.decode(errors="replace") in FUNCS:
                    targets.setdefault(imp.name.decode(errors="replace"), []).append(imp.address)

    text = None
    for s in pe.sections:
        if b".text" in s.Name or b"CODE" in s.Name:
            text = s
            break
    if text is None:
        sys.exit("no .text section")

    data = text.get_data()
    va_start = image_base + text.VirtualAddress
    md = Cs(CS_ARCH_X86, CS_MODE_32)
    md.skipdata = True
    md.detail = True

    slot_set = set(a for lst in targets.values() for a in lst)

    hits = []
    for insn in md.disasm(data, va_start):
        if insn.mnemonic == ".byte":
            continue
        addr = None
        for op in insn.operands:
            if op.type == 3:  # MEM
                mem = op.mem
                if mem.base == 0 and mem.index == 0:
                    addr = mem.disp
                else:
                    addr = None
                if addr is not None and addr in slot_set:
                    break
            else:
                addr = None
        if addr in slot_set:
            hits.append((insn.address, addr))

    lines = ["# sock call sites"]
    lines.append("# IAT slots: " + ", ".join(f"{name}@{hex(a)}" for name, lst in sorted(targets.items()) for a in lst))
    for va, slot in hits:
        name = next(n for n, lst in targets.items() if slot in lst)
        lines.append(f"0x{va - image_base:08x}  call {name}")
    Path(args.out).write_text("\n".join(lines), encoding="utf-8")
    print(f"[+] {len(hits)} call sites -> {args.out}")


if __name__ == "__main__":
    main()
