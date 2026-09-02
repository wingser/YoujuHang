# -*- coding: utf-8 -*-
import re
from pathlib import Path
p = Path(r'D:\Git\YoujuHang\captures\join3_cs_groups.txt')
for enc in ('utf-16', 'utf-16-le', 'utf-8-sig', 'utf-8', 'gb18030'):
    try:
        s = p.read_text(encoding=enc, errors='strict')
        break
    except Exception as e:
        print('enc fail', enc, e)
        s = None
if s:
    for m in re.finditer(r'^--- 组(\d+) 频次=(\d+) sig=(.*)$', s, re.M):
        print(m.group(1).ljust(3), 'freq=' + m.group(2).ljust(5), m.group(3)[:110])
