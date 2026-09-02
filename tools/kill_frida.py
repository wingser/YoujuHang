# -*- coding: utf-8 -*-
"""杀掉后台 frida_attach 进程并清理日志"""
import os
import subprocess
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass


def main():
    out = subprocess.run(["wmic", "process", "where", "name='python.exe'",
                          "get", "ProcessId,CommandLine"],
                         capture_output=True, text=True)
    for line in out.stdout.splitlines():
        if "frida_attach" in line or "frida_multi" in line:
            parts = line.split()
            pid = parts[0]
            subprocess.run(["taskkill", "/PID", pid, "/F"], capture_output=True)
            print("killed", pid)
    for f in [r"d:\Git\YoujuHang\captures\frida_out.txt",
              r"d:\Git\YoujuHang\captures\frida_err.txt",
              os.path.join(os.environ.get("TEMP", r"C:\Windows\Temp"), "h2hook.log")]:
        if os.path.exists(f):
            try:
                os.remove(f)
            except PermissionError:
                print("cannot remove (in use):", f)
    print("cleaned")


if __name__ == "__main__":
    main()
