#!/usr/bin/env python3
import re

with open(r"d:\Git\YoujuHang\captures\mall_page.html", "r", encoding="utf-8", errors="replace") as f:
    html = f.read()

# Find all button/link sections
sections = []
for m in re.finditer(r'<a[^>]*class="btn[^"]*"[^>]*>', html):
    start = max(0, m.start() - 500)
    end = min(len(html), m.end() + 200)
    snippet = html[start:end]
    sections.append(snippet)

print(f"Found {len(sections)} button sections")
for i, s in enumerate(sections):
    print(f"\n=== Button {i+1} ===")
    s = s.replace("\n", " ").replace("\t", " ")
    s = re.sub(r"\s+", " ", s)
    print(s[:800])

# Also search for onclick with package IDs
print("\n=== onclick handlers ===")
for m in re.finditer(r'onclick="([^"]*)"', html):
    onclick = m.group(1)
    if "package" in onclick.lower() or "gift" in onclick.lower() or "get" in onclick.lower():
        start = max(0, m.start() - 300)
        end = min(len(html), m.end() + 100)
        print(f"\n{html[start:end].replace(chr(10), ' ')[:500]}")
