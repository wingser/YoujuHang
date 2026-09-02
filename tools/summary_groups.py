# -*- coding: utf-8 -*-
import io, re, sys
sys.stdout.reconfigure(encoding='utf-8', errors='replace')
s = io.open(r'D:\Git\YoujuHang\captures\join3_cs_groups.txt', encoding='utf-8-sig', errors='replace').read()
for m in re.finditer(r'^--- 组(\d+) 频次=(\d+) sig=(.*)$', s, re.M):
    print(m.group(1).ljust(3), '频次=' + m.group(2).ljust(5), m.group(3)[:110])
