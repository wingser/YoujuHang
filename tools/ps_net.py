# -*- coding: utf-8 -*-
import subprocess
out = subprocess.run(
    ["powershell", "-NoProfile", "-Command",
     "netstat -ano | Select-String '18140|18141|18000|gotvg'"],
    capture_output=True, text=True, encoding="utf-8", errors="replace"
)
print(out.stdout)
# 映射 PID -> 进程名
out2 = subprocess.run(
    ["powershell", "-NoProfile", "-Command",
     "Get-Process | Select-Object Id,ProcessName | Format-Table -AutoSize"],
    capture_output=True, text=True, encoding="utf-8", errors="replace"
)
print(out2.stdout)
