# -*- coding: utf-8 -*-
"""扫描游聚 bin 目录所有 DLL/EXE，按关键词匹配字符串，定位协议实现模块"""
import glob
import os
import re
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

KEYWORDS = sys.argv[1:] or ["encrypt", "md5", "XzoneSign", "SignReq", "gamelogin",
                            "gotvg.com", "http2", "HTTP2", "base64", "AES", "des",
                            "RC4", "cipher", "token"]


def extract_ascii(data, minlen=4):
    return re.findall(rb"[\x20-\x7e]{%d,}" % minlen, data)


def main():
    exe = glob.glob(r"D:/Game/**/bin/X-Zone.exe", recursive=True)
    if not exe:
        print("X-Zone.exe not found")
        return
    base = os.path.dirname(exe[0])
    kws = [k.lower().encode() for k in KEYWORDS]
    targets = []
    for f in os.listdir(base):
        if f.lower().endswith((".dll", ".exe")):
            full = os.path.join(base, f)
            sz = os.path.getsize(full)
            if sz < 100 * 1024 * 1024:  # 跳过超大文件
                targets.append((f, sz))
    targets.sort(key=lambda x: x[1])
    for f, sz in targets:
        full = os.path.join(base, f)
        try:
            data = open(full, "rb").read()
        except Exception:
            continue
        hits = []
        for m in extract_ascii(data, 4):
            low = m.lower()
            if any(k in low for k in kws):
                hits.append(m[:80])
        if hits:
            print("=" * 60)
            print("%s (%.1f MB) - %d hits" % (f, sz / 1048576, len(hits)))
            seen = set()
            for h in hits:
                s = h.decode("ascii", "replace")
                if s not in seen:
                    seen.add(s)
                    print("   ", s)


if __name__ == "__main__":
    main()
