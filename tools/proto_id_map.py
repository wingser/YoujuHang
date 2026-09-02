# -*- coding: utf-8 -*-
"""从 proto/all.proto 提取 消息名 -> ID 映射，并对照流量前缀"""
import io
import re
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

PROTO = r"d:\Git\YoujuHang\proto\all.proto"

def main():
    text = io.open(PROTO, encoding="utf-8").read()
    # 消息块：message Name { ... enum TYPE { ... ID = N; ... } }
    msgs = {}
    for m in re.finditer(r"message\s+(\w+)\s*\{([^}]*?)\}", text, re.S):
        name = m.group(1)
        body = m.group(2)
        idm = re.search(r"ID\s*=\s*(\d+)", body)
        if idm:
            msgs[int(idm.group(1))] = name
    print("Total messages with ID:", len(msgs))
    # 打印感兴趣的 ID 附近
    for mid in sorted(msgs):
        print("%5d = %s" % (mid, msgs[mid]))

if __name__ == "__main__":
    main()
