#!/usr/bin/env python3
import re

with open(r"d:\Git\YoujuHang\captures\mall_page.html", "r", encoding="utf-8", errors="replace") as f:
    html = f.read()

items = []
for m in re.finditer(r'<div class="package_item" id="package_(\d+)">', html):
    pkg_id = m.group(1)
    start = m.start()
    end = min(len(html), m.end() + 800)
    snippet = html[start:end]
    id_match = re.search(r'<div class="id" style="display:none">(\d+)</div>', snippet)
    hidden_id = id_match.group(1) if id_match else "unknown"
    name_match = re.search(r'alt="([^"]+)"', snippet)
    name = name_match.group(1) if name_match else "unknown"
    items.append((pkg_id, hidden_id, name))
    print(f"package_item id={pkg_id}, hidden_id={hidden_id}, name={name}")

print(f"\nTotal packages: {len(items)}")
