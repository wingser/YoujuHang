# -*- coding: utf-8 -*-
"""实验：验证账号在线时再次登录的服务器行为，以及被顶 token 是否仍可用
场景模拟：程序(MAC_A)挂机 -> 用户(MAC_B)登录顶线 -> 程序旧 token 探测
用法: python probe_online.py <account> <password>
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


def login(account, pwd, mac, tag):
    print("\n== %s 登录 (mac=%s) ==" % (tag, mac))
    body = build_login_body(account, pwd, mac)
    _, resp = h2_post(LOGIN_HOST, LOGIN_PORT, "/login/", body)
    try:
        dec = b64dec(resp.decode("ascii"))
    except Exception as e:
        print("   resp decode fail:", e, resp.hex())
        return None
    f = proto_parse(dec)
    print("   ret=%r" % (f.get(1),))
    if f.get(1, ("varint", 0))[1] != 1:
        err = f.get(2, ("bytes", b""))[1]
        print("   FAIL errStr=%r" % err)
        return None
    uid = f[3][1]
    token = f[4][1].decode("ascii")
    gate_host = gate_port = None
    if 5 in f:
        gf = proto_parse(f[5][1])
        addr = gf.get(1, ("bytes", b""))[1].decode("ascii", "replace")
        if ":" in addr:
            gate_host, gate_port = addr.rsplit(":", 1)
            gate_port = int(gate_port)
    print("   uid=%d token=%.24s... gate=%s:%s" % (uid, token, gate_host, gate_port))
    # 打印全部字段，找"已在线"提示
    for fn, (wt, v) in sorted(f.items()):
        if wt == "bytes" and all(32 <= b < 127 for b in v) and v:
            print("   f%d=%r" % (fn, v.decode("ascii", "replace")))
    return {"uid": uid, "token": token, "gate": (gate_host, gate_port)}


def game(sess, msgid, tag):
    if not sess or not sess.get("gate") or not sess["gate"][0]:
        print("   [%s] 无 gate 信息，跳过" % tag)
        return None
    host, port = sess["gate"]
    proto = b"\x08" + varint(sess["uid"]) + b"\x12" + bytes([len(sess["token"])]) + sess["token"].encode("ascii")
    body = b64url(struct.pack("<H", msgid) + proto)
    try:
        headers, resp = h2_post(host, port, "/game/", body)
        status = [v for k, v in headers if k == ":status"]
    except Exception as e:
        print("   [%s] game(%d) 连接失败: %s" % (tag, msgid, e))
        return None
    print("   [%s] game(%d) status=%s body(%dB)=%s" % (tag, msgid, status, len(resp), resp[:80].hex()))
    try:
        dec = b64dec(resp.decode("ascii"))
    except Exception:
        return None
    f = proto_parse(dec)
    ret = f.get(1, ("varint", -1))[1]
    print("   [%s] game(%d) ret=%s" % (tag, msgid, ret))
    return f


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return
    account, pwd = sys.argv[1], sys.argv[2]

    s1 = login(account, pwd, rand_mac(), "A-hangup")
    if not s1:
        return
    game(s1, 1172, "A-request")

    s2 = login(account, pwd, rand_mac(), "B-user-online")
    if not s2:
        return
    game(s2, 1172, "B-request")

    print("\n== KEY TEST: old token A reconnect gate ==")
    game(s1, 1172, "A-probe-after-kick")

    print("\n== KEY TEST: A re-login (program recover) ==")
    s3 = login(account, pwd, rand_mac(), "A2-recover")
    if s3:
        game(s3, 1172, "A2-request")
    print("\n== 实验结束 ==")


if __name__ == "__main__":
    main()
