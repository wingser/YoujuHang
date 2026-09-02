# -*- coding: utf-8 -*-
"""从 X-Zone.exe 提取 ASCII/UTF-16 字符串并搜索关键词"""
import re
import sys
import io

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

EXE = r"D:\Game\游聚平台\1\bin\X-Zone.exe"

def extract_ascii(data, minlen=4):
    return re.findall(rb"[\x20-\x7e]{%d,}" % minlen, data)

def extract_utf16(data, minlen=4):
    out = []
    for m in re.finditer(rb"(?:[\x20-\x7e]\x00){%d,}" % minlen, data):
        out.append(m.group(0).decode("utf-16-le", errors="replace"))
    return out

def main():
    kws = sys.argv[1:]
    if not kws:
        kws = ["DAILY_SIGN", "MONTH_SIGN", "GUILD_SIGN", "checkInType", "CheckIn", "SignIn", "XZONE_SIGN", "xzone_sign", "XzoneSign", "SRAM"]
    data = open(EXE, "rb").read()
    print("exe size:", len(data))
    for kw in kws:
        hits = []
        # ASCII
        for s in extract_ascii(data):
            if kw.lower() in s.decode(errors="replace").lower():
                hits.append(s.decode(errors="replace"))
        # UTF-16
        for s in extract_utf16(data):
            if kw.lower() in s.lower():
                hits.append(s)
        print("\n=== %s ===" % kw)
        for h in hits[:30]:
            print("  ", h)

if __name__ == "__main__":
    main()
