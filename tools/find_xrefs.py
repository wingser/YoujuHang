#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""32 位 PE 中查找对指定 VA 的直接 call/jmp xref。

用法: python find_xrefs.py <pe> <va1> [va2 ...]
"""
import glob
import sys
from pathlib import Path

import pefile
from capstone import Cs, CS_ARCH_X86, CS_MODE_32


def main():
    if len(sys.argv) < 3:
        sys.exit("usage: find_xrefs.py <pe> <va1> [va2 ...]")
    pe_path = sys.argv[1]
    if any(c in pe_path for c in "*?"):
        m = glob.glob(pe_path)
        if not m:
            sys.exit("no file")
        pe_path = m[0]
    targets = {int(x, 16): i for i, x in enumerate(sys.argv[2:])}

    pe = pefile.PE(pe_path, fast_load=True)
    img = pe.OPTIONAL_HEADER.ImageBase
    text = next(s for s in pe.sections if b".text" in s.Name)
    data = text.get_data()
    md = Cs(CS_ARCH_X86, CS_MODE_32)
    md.skipdata = True
    md.detail = True

    results = {i: [] for i in targets.values()}
    for insn in md.disasm(data, img + text.VirtualAddress):
        if insn.mnemonic == ".byte":
            continue
        for op in insn.operands:
            if op.type == 2 and op.imm in targets:  # IMM
                results[targets[op.imm]].append(insn.address)
    for i, va in enumerate(sys.argv[2:]):
        print(f"xrefs to {va}: {len(results[i])}")
        for a in results[i][:40]:
            print(f"  0x{a - img:08x}  (file rva)")


if __name__ == "__main__":
    main()
