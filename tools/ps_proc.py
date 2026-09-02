# -*- coding: utf-8 -*-
import subprocess
out = subprocess.run(
    ["powershell", "-NoProfile", "-Command",
     "Get-Process | Where-Object { $_.ProcessName -like '*zone*' -or $_.ProcessName -like '*gotvg*' } | Select-Object Id,ProcessName,StartTime | Format-List"],
    capture_output=True, text=True, encoding="utf-8", errors="replace"
)
print(out.stdout)
print(out.stderr)
