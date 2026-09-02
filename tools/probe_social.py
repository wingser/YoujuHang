# -*- coding: utf-8 -*-
"""实验：探测 PlayerSocialCMsg(1176)，找到获取好友/用户在线状态的 action
用法: python probe_social.py <account> <password> [target_uid...]
"""
import base64
import hashlib
import random
import socket
import struct
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

from h2.config import H2Configuration
from h2.connection import H2Connection
from h2.events import DataReceived, ResponseReceived, StreamEnded

LOGIN_HOST = "gamelogin3.gotvg.com"
LOGIN_PORT = 18000


def varint(n):
    out = bytearray()
    while True:
        b = n & 0x7F
        n >>= 7
        if n:
            out.append(b | 0x80)
        else:
            out.append(b)
            return bytes(out)


def uvarint(buf, off):
    v = 0
    s = 0
    while True:
        x = buf[off]
        off += 1
        v |= (x & 0x7F) << s
        if not x & 0x80:
            break
        s += 7
    return v, off


def proto_parse(buf):
    out = {}
    off = 0
    while off < len(buf):
        tag, off = uvarint(buf, off)
        fn, wt = tag >> 3, tag & 7
        if wt == 0:
            v, off = uvarint(buf, off)
            out[fn] = ("varint", v)
        elif wt == 2:
            ln, off = uvarint(buf, off)
            v = buf[off:off + ln]
            off += ln
            if fn in out:
                prev = out[fn]
                if prev[0] == "bytes_list":
                    out[fn] = (prev[0], prev[1] + [v])
                else:
                    out[fn] = ("bytes_list", [prev[1], v])
            else:
                out[fn] = ("bytes", v)
        elif wt == 1:
            out[fn] = ("fixed64", buf[off:off + 8])
            off += 8
        elif wt == 5:
            out[fn] = ("fixed32", buf[off:off + 4])
            off += 4
        else:
            break
    return out


def b64url(b):
    return base64.urlsafe_b64encode(b).decode("ascii")


def b64dec(s):
    return base64.urlsafe_b64decode(s + "=" * ((-len(s)) % 4))


def rand_mac():
    return "00-16-3E-%02X-%02X-%02X" % (
        random.randint(0, 255), random.randint(0, 255), random.randint(0, 255))


def build_login_body(account, pwd, mac):
    md5 = hashlib.md5(pwd.encode("utf-8")).hexdigest().upper()
    proto = b""
    proto += b"\x12" + bytes([len(account)]) + account.encode("ascii")
    proto += b"\x1a" + bytes([len(md5)]) + md5.encode("ascii")
    proto += b"\x22" + bytes([len(mac)]) + mac.encode("ascii")
    raknet = random.getrandbits(64) | 0x1000000000000000
    proto += b"\x30" + varint(raknet)
    return b64url(b"\x00\x01" + proto)


def h2_post(host, port, path, body_b64):
    sock = socket.create_connection((host, port), timeout=12)
    config = H2Configuration(client_side=True, header_encoding="utf-8")
    conn = H2Connection(config=config)
    conn.initiate_connection()
    sock.sendall(conn.data_to_send())
    headers = [
        (":method", "POST"), (":scheme", "http"), (":path", path),
        (":authority", "%s:%d" % (host, port)),
        ("content-type", "application/x-www-form-urlencoded"),
        ("content-length", str(len(body_b64))),
        ("user-agent", "Go-http-client/2.0"),
    ]
    sid = conn.get_next_available_stream_id()
    conn.send_headers(sid, headers, end_stream=False)
    conn.send_data(sid, body_b64.encode("ascii"), end_stream=True)
    sock.sendall(conn.data_to_send())
    resp_headers = []
    resp_body = b""
    ended = False
    while not ended:
        try:
            data = sock.recv(65535)
        except Exception:
            break
        if not data:
            break
        events = conn.receive_data(data)
        for ev in events:
            if isinstance(ev, ResponseReceived):
                resp_headers = [(k, v) for k, v in ev.headers]
            elif isinstance(ev, DataReceived):
                resp_body += ev.data
            elif isinstance(ev, StreamEnded) and ev.stream_id == sid:
                ended = True
        sock.sendall(conn.data_to_send())
    sock.close()
    return resp_headers, resp_body


def login(account, pwd):
    body = build_login_body(account, pwd, rand_mac())
    _, resp = h2_post(LOGIN_HOST, LOGIN_PORT, "/login/", body)
    dec = b64dec(resp.decode("ascii"))
    f = proto_parse(dec)
    if f.get(1, ("varint", 0))[1] != 1:
        print("登录失败", f)
        return None
    uid = f[3][1]
    token = f[4][1].decode("ascii")
    gf = proto_parse(f[5][1])
    addr = gf.get(1, ("bytes", b""))[1].decode("ascii", "replace")
    host, port = addr.rsplit(":", 1)
    print("登录 ok: uid=%d gate=%s:%s" % (uid, host, port))
    return {"uid": uid, "token": token, "gate": (host, int(port))}


def dump(buf, indent="  ", depth=0):
    """完整打印嵌套 protobuf 所有字段（含重复字段）"""
    off = 0
    out = []
    while off < len(buf):
        tag, off = uvarint(buf, off)
        fn, wt = tag >> 3, tag & 7
        if wt == 0:
            v, off = uvarint(buf, off)
            out.append("f%d varint=%d" % (fn, v))
        elif wt == 2:
            ln, off = uvarint(buf, off)
            v = buf[off:off + ln]
            off += ln
            printable = all(32 <= b < 127 for b in v) and v
            if printable:
                out.append("f%d str=%r" % (fn, v.decode("ascii", "replace")))
            else:
                out.append("f%d bytes(%d)=%s" % (fn, len(v), v.hex()))
        elif wt == 1:
            out.append("f%d fixed64=%s" % (fn, buf[off:off + 8].hex()))
            off += 8
        elif wt == 5:
            out.append("f%d fixed32=%s" % (fn, buf[off:off + 4].hex()))
            off += 4
        else:
            out.append("f%d wt%d??" % (fn, wt))
            break
    for line in out:
        print(indent * (depth + 1) + line)


def social(sess, action, uid_list, target_uid, tag):
    proto = b"\x08" + varint(sess["uid"]) + b"\x12" + bytes([len(sess["token"])]) + sess["token"].encode("ascii")
    proto += b"\x18" + varint(action)
    for u in uid_list:
        proto += b"\x20" + varint(u)
    if target_uid:
        proto += b"\x28" + varint(target_uid)
    body = b64url(struct.pack("<H", 1176) + proto)
    host, port = sess["gate"]
    _, resp = h2_post(host, port, "/game/", body)
    try:
        dec = b64dec(resp.decode("ascii"))
    except Exception as e:
        print("[%s] action=%s resp parse fail %s" % (tag, action, e))
        return
    f = proto_parse(dec)
    print("[%s] action=%s uidList=%s target=%s" % (tag, action, uid_list, target_uid))
    for fn, (wt, v) in sorted(f.items()):
        if wt == "bytes_list":
            print("   f%d = [%d items]" % (fn, len(v)))
            for i, item in enumerate(v):
                print("     [%d] " % i, end="")
                dump(item, depth=1)
        elif wt == "bytes":
            printable = all(32 <= b < 127 for b in v) and v
            if printable:
                print("   f%d=%r" % (fn, v.decode("ascii", "replace")))
            else:
                print("   f%d bytes(%d)=" % (fn, len(v)))
                dump(v, depth=1)
        else:
            print("   f%d %s=%s" % (fn, wt, v))


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return
    account, pwd = sys.argv[1], sys.argv[2]
    targets = [int(x) for x in sys.argv[3:]]

    sess = login(account, pwd)
    if not sess:
        return
    print("\n-- 查询自己（必然在线，对照 isOnline 是否返回） --")
    social(sess, 1, [sess["uid"]], 0, "self")
    if targets:
        print("\n-- 带 uidList 查询 --")
        for a in (1, 3):
            social(sess, a, targets, 0, "uidlist")
        print("\n-- 带 targetUid 查询 --")
        for a in (1, 3):
            social(sess, a, [], targets[0], "target")
    print("\n== 结束 ==")


if __name__ == "__main__":
    main()
