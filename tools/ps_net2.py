# -*- coding: utf-8 -*-
import subprocess

pids = {"9888", "8352", "6872"}
out = subprocess.run(
    ["netstat", "-ano"],
    capture_output=True, text=True, encoding="utf-8", errors="replace"
)
for line in out.stdout.splitlines():
    parts = line.split()
    if len(parts) >= 5 and parts[-1] in pids:
        print(line)
