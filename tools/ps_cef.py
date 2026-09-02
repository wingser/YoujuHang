# -*- coding: utf-8 -*-
"""列出 gotvg_web 和 X-Zone 的子进程（CEF 多进程架构）"""
import subprocess
import json

ps = subprocess.run(
    ["powershell", "-NoProfile", "-Command",
     "Get-CimInstance Win32_Process | Select-Object ProcessId,ParentProcessId,Name,CommandLine | ConvertTo-Json -Compress"],
    capture_output=True, text=True, encoding="utf-8", errors="replace").stdout
try:
    procs = json.loads(ps)
    if isinstance(procs, dict):
        procs = [procs]
except Exception:
    procs = []

roots = {"8352": "gotvg_web", "9888": "X-Zone", "6872": "XZDesktop64"}
all_procs = {str(p.get("ProcessId")): p for p in procs}

# BFS 找子树
def subtree(pid, depth=0):
    if depth > 4:
        return
    print("  " * depth + f"{pid}  {all_procs.get(pid,{}).get('Name','?')}")
    cmd = (all_procs.get(pid, {}).get("CommandLine") or "")[:120]
    if cmd:
        print("  " * depth + f"     cmd: {cmd}")
    for p in procs:
        if str(p.get("ParentProcessId")) == str(pid):
            subtree(str(p.get("ProcessId")), depth + 1)

for pid, name in roots.items():
    print(f"=== {name} ({pid}) tree ===")
    subtree(pid)
    print()
