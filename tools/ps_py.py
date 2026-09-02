# -*- coding: utf-8 -*-
import subprocess
out = subprocess.run(
    ["powershell", "-NoProfile", "-Command",
     "Get-Process python -ErrorAction SilentlyContinue | Select-Object Id,StartTime,Path | Format-List"],
    capture_output=True, text=True, encoding="utf-8", errors="replace"
)
print(out.stdout or "no python procs")
