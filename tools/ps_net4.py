# -*- coding: utf-8 -*-
"""列出与 153.99.234.163 / 153.0.226.187 相关的所有连接及其 PID"""
import subprocess

net = subprocess.run(["netstat", "-ano"], capture_output=True, text=True,
                     encoding="utf-8", errors="replace").stdout
pids = {}
for line in net.splitlines():
    if "153.99.234.163" in line or "153.0.226.187" in line or "192.168.224.205" in line:
        parts = line.split()
        if len(parts) >= 5:
            pids.setdefault(parts[4], []).append(line)

out2 = subprocess.run(
    ["powershell", "-NoProfile", "-Command",
     "Get-Process | Select-Object Id,ProcessName | ConvertTo-Json -Compress"],
    capture_output=True, text=True, encoding="utf-8", errors="replace").stdout
import json
try:
    procs = json.loads(out2)
    if isinstance(procs, dict):
        procs = [procs]
    name_map = {str(p["Id"]): p["ProcessName"] for p in procs}
except Exception:
    name_map = {}

for pid, lines in pids.items():
    print(f"PID {pid} ({name_map.get(pid,'?')}):")
    for l in lines:
        print("   ", l)
