# -*- coding: utf-8 -*-
"""观测目标账号会话 TTL：每 N 秒用侦查账号查询目标在线状态，记录时间戳。
用法: python ttl_observe.py <probe_account> <password> <target_uid> [interval_sec] [rounds]
"""
import os
import re
import subprocess
import sys
import time

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

BASE = os.path.dirname(os.path.abspath(__file__))


def main():
    if len(sys.argv) < 4:
        print(__doc__)
        return
    probe, pwd = sys.argv[1], sys.argv[2]
    target = sys.argv[3]
    interval = int(sys.argv[4]) if len(sys.argv) > 4 else 300
    rounds = int(sys.argv[5]) if len(sys.argv) > 5 else 16
    for i in range(rounds):
        ts = time.strftime("%H:%M:%S")
        try:
            out = subprocess.run(
                [sys.executable, os.path.join(BASE, "probe_social.py"), probe, pwd, target],
                capture_output=True, text=True, encoding="utf-8", errors="replace",
                timeout=90,
            )
            text = out.stdout
            online = "query-fail"
            idx = text.find("f1 varint=" + target)
            if idx >= 0:
                seg = text[idx:]
                m = re.search(r"f100 varint=(\d+)", seg)
                online = ("online(1)" if m.group(1) == "1" else "offline(0)") if m else "no-isOnline"
            elif "登录失败" in text:
                online = "probe-login-fail"
            print("%s [%02d] target=%s" % (ts, i, online), flush=True)
        except Exception as e:
            print("%s [%02d] error: %s" % (ts, i, e), flush=True)
        time.sleep(interval)


if __name__ == "__main__":
    main()
