# -*- coding: utf-8 -*-
"""分析 sock=4444 的所有 HEADERS 密文块，寻找 XOR/流密码模式"""
import io
import re
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

LOG = r"C:\Users\Lenovo\AppData\Local\Temp\h5_xzone.log"

def hexs_to_bytes(hs):
    try:
        return bytes.fromhex(hs.replace(" ", "").replace("\n", ""))
    except Exception:
        return None

def main():
    lines = io.open(LOG, encoding="utf-8", errors="replace").readlines()
    headers = []  # (t, dir, block)
    i = 0
    while i < len(lines):
        l = lines[i]
        m = re.search(r"\[(\d+)\] (SEND|RECV) sock=(\d+) tag=ws2_32.dll!WSASend bufCount=1 WSABUF.len=(\d+)", l)
        if m and int(m.group(3)) == 4444:
            t = int(m.group(1))
            dr = m.group(2)
            data_lines = []
            j = i + 1
            while j < len(lines) and not lines[j].startswith("["):
                data_lines.append(lines[j])
                j += 1
            raw = "".join(data_lines)
            b = hexs_to_bytes(raw)
            if b:
                # 解析 H2 帧
                off = 0
                while off + 9 <= len(b):
                    length = (b[off] << 16) | (b[off+1] << 8) | b[off+2]
                    ftype = b[off+3]
                    flags = b[off+4]
                    stream = (b[off+5] << 24) | (b[off+6] << 16) | (b[off+7] << 8) | b[off+8]
                    body = b[off+9:off+9+length]
                    if ftype == 1:  # HEADERS
                        headers.append((t, dr, stream, body))
                    off += 9 + length
            i = j
        else:
            i += 1

    print("HEADERS blocks found:", len(headers))
    for t, dr, stream, block in headers:
        print("t=%d %s stream=%d len=%d: %s" % (t, dr, stream, len(block), block.hex()))

    # 两两 XOR 分析共享最长公共子串
    print("\n=== common substrings between blocks ===")
    for a in range(len(headers)):
        for b in range(a+1, len(headers)):
            x = headers[a][3]
            y = headers[b][3]
            # 找最长公共子串
            lcs = ""
            for i in range(len(x)):
                for j in range(len(y)):
                    k = 0
                    while i+k < len(x) and j+k < len(y) and x[i+k] == y[j+k]:
                        k += 1
                    if k > len(lcs):
                        lcs = x[i:i+k]
            if len(lcs) >= 4:
                print("block[%d](t=%d) & block[%d](t=%d): common=%d bytes: %s" % (
                    a, headers[a][0], b, headers[b][0], len(lcs), lcs.hex()))

if __name__ == "__main__":
    main()
