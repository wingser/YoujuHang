#!/usr/bin/env python3
"""登录后查询 UserInfoCMsg(514) / RefreshInfoCMsg(515)，解析等级/经验字段"""
import base64
import hashlib
import socket
import struct
import sys

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
        if x & 0x80 == 0:
            return v, off
        s += 7


def parse_fields(buf):
    fields = []
    off = 0
    while off < len(buf):
        tag, off = uvarint(buf, off)
        num, wire = tag >> 3, tag & 7
        if wire == 0:
            v, off = uvarint(buf, off)
            fields.append((num, wire, v))
        elif wire == 1:
            fields.append((num, wire, buf[off:off+8]))
            off += 8
        elif wire == 2:
            l, off = uvarint(buf, off)
            fields.append((num, wire, buf[off:off+l]))
            off += l
        elif wire == 5:
            fields.append((num, wire, buf[off:off+4]))
            off += 4
        else:
            break
    return fields


def b64url(b):
    return base64.urlsafe_b64encode(b).decode("ascii")


def h2_request(host, port, path, body_b64):
    if isinstance(body_b64, str):
        body_b64 = body_b64.encode("ascii")
    sock = socket.create_connection((host, port), timeout=10)
    config = H2Configuration(client_side=True, header_encoding="utf-8")
    conn = H2Connection(config=config)
    conn.initiate_connection()
    sock.sendall(conn.data_to_send())
    headers = [
        (":method", "POST"),
        (":scheme", "http"),
        (":path", path),
        (":authority", "%s:%d" % (host, port)),
        ("content-type", "application/x-www-form-urlencoded"),
        ("content-length", str(len(body_b64))),
        ("user-agent", "Go-http-client/2.0"),
    ]
    stream_id = conn.get_next_available_stream_id()
    conn.send_headers(stream_id, headers, end_stream=False)
    conn.send_data(stream_id, body_b64, end_stream=True)
    sock.sendall(conn.data_to_send())
    resp_headers = []
    resp_body = b""
    stream_ended = False
    while not stream_ended:
        data = sock.recv(65535)
        if not data:
            break
        events = conn.receive_data(data)
        for ev in events:
            if isinstance(ev, ResponseReceived):
                resp_headers = [(k, v) for k, v in ev.headers]
            elif isinstance(ev, DataReceived):
                resp_body += ev.data
            elif isinstance(ev, StreamEnded) and ev.stream_id == stream_id:
                stream_ended = True
        sock.sendall(conn.data_to_send())
    sock.close()
    return resp_headers, resp_body


def pretty(fields, indent=0):
    for n, w, v in fields:
        if w == 2:
            inner = parse_fields(v)
            if inner:
                print(" " * indent + "f%d message:" % n)
                pretty(inner, indent + 2)
            else:
                # try as string
                try:
                    s = v.decode("ascii")
                    if all(32 <= c < 127 for c in v):
                        print(" " * indent + "f%d string=%r" % (n, s))
                        continue
                except:
                    pass
                print(" " * indent + "f%d bytes(%d)=%s" % (n, len(v), v.hex()))
        else:
            print(" " * indent + "f%d varint=%d" % (n, v))


def main():
    if len(sys.argv) < 3:
        print("usage: query_userinfo.py account password")
        return
    account, pwd = sys.argv[1], sys.argv[2]

    md5 = hashlib.md5(pwd.encode("utf-8")).hexdigest().upper()
    mac = "54-05-DB-91-34-AB"
    proto = b"\x12" + bytes([len(account)]) + account.encode("ascii")
    proto += b"\x1a" + bytes([len(md5)]) + md5.encode("ascii")
    proto += b"\x22" + bytes([len(mac)]) + mac.encode("ascii")
    proto += b"\x30" + varint(0x1234567890)
    body = b64url(b"\x00\x01" + proto)
    h, b = h2_request(LOGIN_HOST, LOGIN_PORT, "/login/", body)
    s = b.decode("ascii")
    dec = base64.urlsafe_b64decode(s + "=" * ((-len(s)) % 4))
    fields = parse_fields(dec)
    uid = [v for n, w, v in fields if n == 3][0]
    token = [v for n, w, v in fields if n == 4][0].decode("ascii")
    gate = None
    for n, w, v in fields:
        if n == 5 and w == 2:
            gf = parse_fields(v)
            for gn, gw, gv in gf:
                if gn == 1:
                    gate = gv.decode("ascii")
    print("登录成功 uid=%s gate=%s" % (uid, gate))
    ghost, gport = gate.rsplit(":", 1)

    for msgid, name in [(514, "UserInfoCMsg"), (515, "RefreshInfoCMsg")]:
        extra = b"\x18\x05" if msgid == 515 else b""
        proto2 = b"\x08" + varint(uid) + b"\x12" + bytes([len(token)]) + token.encode("ascii") + extra
        body2 = b64url(struct.pack("<H", msgid) + proto2)
        h2, b2 = h2_request(ghost, int(gport), "/game/", body2)
        s2 = b2.decode("ascii")
        dec2 = base64.urlsafe_b64decode(s2 + "=" * ((-len(s2)) % 4))
        print("\n=== %s(%d) 响应 %d B ===" % (name, msgid, len(dec2)))
        print("hex:", dec2.hex())
        f2 = parse_fields(dec2)
        pretty(f2)
        # 查找 EXP / 经验 / 等级关键词
        for kw in [b"EXP", b"exp", b"lv", b"level"]:
            if kw in dec2:
                print(">>> 包含关键词: %s" % kw)


if __name__ == "__main__":
    main()
