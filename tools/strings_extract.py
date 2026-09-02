# -*- coding: utf-8 -*-
"""从 PE 文件提取字符串（ASCII + UTF-16LE），支持关键词过滤

用法:
  python tools/strings_extract.py scan <pe_path> [keyword...]
     扫描全文件，打印含关键词的字符串上下文
  python tools/strings_extract.py strings <pe_path> <minlen> [outfile]
     导出所有字符串到文件
"""
import re
import sys
import os

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def find_zone_bin():
    """通过 glob 找到 X-Zone.exe 路径（避免中文参数编码问题）"""
    import glob
    for m in glob.glob(r"D:/Game/**/bin/X-Zone.exe", recursive=True):
        return m
    for m in glob.glob(r"D:/**/X-Zone.exe", recursive=True):
        return m
    return None


def extract_strings(path, minlen=6):
    data = open(path, "rb").read()
    ascii_re = re.compile(rb"[\x20-\x7e]{%d,}" % minlen)
    utf16_re = re.compile((r"(?:[\x20-\x7e]\x00){%d,}" % minlen).encode())
    out = []
    for m in ascii_re.finditer(data):
        s = m.group().decode("ascii", "replace")
        out.append((m.start(), "ascii", s))
    for m in utf16_re.finditer(data):
        s = m.group().decode("utf-16le", "replace")
        out.append((m.start(), "utf16", s))
    out.sort(key=lambda x: x[0])
    return out


def cmd_scan(path, keywords):
    print("scanning", path, "for", keywords)
    out = extract_strings(path, minlen=4)
    kws = [k.lower() for k in keywords]
    count = 0
    for off, kind, s in out:
        low = s.lower()
        if any(k in low for k in kws):
            print("0x%08X [%s] %r" % (off, kind, s[:300]))
            count += 1
            if count >= 200:
                print("... (truncated at 200)")
                break
    print("total matches shown:", count)


def cmd_strings(path, minlen, outfile):
    out = extract_strings(path, int(minlen))
    if outfile:
        with open(outfile, "w", encoding="utf-8") as f:
            for off, kind, s in out:
                f.write("0x%08X\t%s\t%s\n" % (off, kind, s))
        print("wrote %d strings to %s" % (len(out), outfile))
    else:
        for off, kind, s in out[:100]:
            print("0x%08X [%s] %r" % (off, kind, s[:200]))


if __name__ == "__main__":
    if len(sys.argv) < 3:
        print(__doc__)
        sys.exit(1)
    cmd = sys.argv[1]
    path = sys.argv[2]
    if not os.path.exists(path):
        found = find_zone_bin()
        if found:
            path = found
            print("using detected path:", path)
        else:
            print("file not found:", path)
            sys.exit(1)
    if cmd == "scan":
        cmd_scan(path, sys.argv[3:])
    elif cmd == "strings":
        minlen = sys.argv[3] if len(sys.argv) > 3 else "6"
        outfile = sys.argv[4] if len(sys.argv) > 4 else None
        cmd_strings(path, minlen, outfile)
    else:
        print("unknown cmd:", cmd)
