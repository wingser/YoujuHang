# -*- coding: utf-8 -*-
"""查进程父子树 + 相关进程连接"""
import subprocess
import json

ps = subprocess.run(
    ["powershell", "-NoProfile", "-Command",
     "Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,ExecutablePath,CommandLine | ConvertTo-Json -Compress"],
    capture_output=True, text=True, encoding="utf-8", errors="replace").stdout
try:
    procs = json.loads(ps)
    if isinstance(procs, dict):
        procs = [procs]
except Exception as e:
    print("json err:", e)
    procs = []

# 关注关键字
keywords = ["zone", "gotvg", "youju", "kof", "emul", "x-zone", "xzdesktop", "CEF", "cef"]
print("=== 相关进程 ===")
for p in procs:
    name = (p.get("Name") or "")
    path = (p.get("ExecutablePath") or "")
    cmd = (p.get("CommandLine") or "")[:150]
    if any(k.lower() in (name + " " + path + " " + cmd).lower() for k in keywords):
        print(f"PID {p.get('ProcessId')}  PPID {p.get('ParentProcessId')}  {name}")
        if path:
            print(f"    path: {path}")
        if cmd:
            print(f"    cmd: {cmd}")

# 连接
print("\n=== 所有 ESTABLISHED + 相关 PID ===")
net = subprocess.run(["netstat", "-ano"], capture_output=True, text=True,
                     encoding="utf-8", errors="replace").stdout
pids = {}
for line in net.splitlines():
    parts = line.split()
    if len(parts) >= 5 and parts[0] == "TCP":
        pids.setdefault(parts[4], []).append(" ".join(parts[1:5]))
for pid, lines in pids.items():
    if lines and any(k in str(procs) for k in []):
        pass
# 简单输出所有 TCP 连接（数量少）
for pid, lines in sorted(pids.items(), key=lambda kv: -len(kv[1])):
    print(f"PID {pid} ({len(lines)}):")
    for l in lines[:8]:
        print("   ", l)
