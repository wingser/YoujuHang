#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""用字节扫描方式查找 32 位 PE 中对指定 VA 的 E8 (call rel32) 引用。
注意：E8 可能出现在数据中（对齐噪声），调用点需人工复核。
用法: python find_e8_xrefs.py <pe> <va1> [va2 ...]  (va 为绝对地址)
"""
import glob
import struct
import sys

import pefile


def main():
    if len(sys.argv) < 3:
        sys.exit("usage: find_e8_xrefs.py <pe> <va1> [va2 ...]")
    pe_path = sys.argv[1]
    if any(c in pe_path for c in "*?"):
        m = glob.glob(pe_path)
        if not m:
            sys.exit("no file")
        pe_path = m[0]
    targets = {int(x, 16) for x in sys.argv[2:]}

    pe = pefile.PE(pe_path, fast_load=True)
    img = pe.OPTIONAL_HEADER.ImageBase
    text = next(s for s in pe.sections if b".text" in s.Name)
    raw_off = text.PointerToRawData
    rva_off = text.VirtualAddress
    data = text.get_data()
    hits = {t: [] for t in targets}
    i = 0
    while True:
        j = data.find(b"\xe8", i)
        if j < 0:
            break
        if j + 5 <= len(data):
            rel = struct.unpack("<i", data[j + 1:j + 5])[0]
            va = img + rva_off + j
            target = va + 5 + rel
            if target in targets:
                hits[target].append(va)
        i = j + 1
    for t in sorted(targets):
        print(f"E8 xrefs to 0x{t:08x} (VA) / 0x{t-img:08x} (RVA): {len(hits[t])}")
        for a in hits[t][:50]:
            print(f"   0x{a:08x}  (rva 0x{a-img:08x})")


if __name__ == "__main__":
    main()
