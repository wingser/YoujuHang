# -*- coding: utf-8 -*-
"""简单顶线：登录目标账号并连 gate 发一次任务请求（模拟用户客户端登录），
登录成功即使目标账号旧会话失效，进程退出后服务器仍记录该账号在线。"""
import sys
from probe_online import login, game, rand_mac

if __name__ == "__main__":
    if len(sys.argv) < 3:
        print("usage: python simple_kick.py <account> <password>")
        sys.exit(1)
    s = login(sys.argv[1], sys.argv[2], rand_mac(), "KICK")
    if s:
        game(s, 1172, "KICK-request")
        print("kick done, session token left: %s" % s["token"][:16])
