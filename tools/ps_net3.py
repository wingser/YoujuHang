# -*- coding: utf-8 -*-
"""列出所有 ESTABLISHED TCP 连接及 PID，并对照进程名"""
import subprocess

out = subprocess.run(["netstat", "-ano"], capture_output=True, text=True,
                     encoding="utf-8", errors="replace").stdout
pids = {}
for line in out.splitlines():
    parts = line.split()
    if len(parts) >= 5 and parts[0] == "TCP" and "ESTABLISHED" in line:
        pids.setdefault(parts[4], []).append(line)

# 进程名映射
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
except Exception as e:
    print("json err", e)
    name_map = {}

# 只显示有连接的进程
for pid, lines in sorted(pids.items(), key=lambda kv: -len(kv[1])):
    print(f"PID {pid} ({name_map.get(pid, '?')}): {len(lines)} connections")
    for l in lines[:6]:
        print("  ", l)
