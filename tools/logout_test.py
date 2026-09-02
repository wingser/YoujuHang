# -*- coding: utf-8 -*-
"""验证登出链路：登录(顶线) -> 发 LeaveZoneCMsg(534) 登出 -> 服务器清除会话"""
import base64
import struct
import sys
from probe_online import login, game, rand_mac, b64url, h2_post, varint

CMsgLeaveZone = 534


def logout(sess, tag):
    proto = b"\x08" + varint(sess["uid"]) + b"\x12" + bytes([len(sess["token"])]) + sess["token"].encode("ascii")
    body = b64url(struct.pack("<H", CMsgLeaveZone) + proto)
    host, port = sess["gate"]
    _, resp = h2_post(host, port, "/game/", body)
    try:
        dec = base64.urlsafe_b64decode(resp.decode("ascii") + "=" * ((-len(resp.decode("ascii"))) % 4))
        print("[%s] LeaveZone resp hex=%s" % (tag, dec.hex()[:60]))
    except Exception as e:
        print("[%s] LeaveZone resp raw=%r err=%s" % (tag, resp[:60], e))


if __name__ == "__main__":
    if len(sys.argv) < 3:
        print("usage: python logout_test.py <account> <password>")
        sys.exit(1)
    s = login(sys.argv[1], sys.argv[2], rand_mac(), "LOGOUT")
    if s:
        game(s, 1172, "LOGOUT-before")
        logout(s, "LOGOUT")
        print("logout sent")
