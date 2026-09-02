# -*- coding: utf-8 -*-
"""完整登录闭环验证：HTTP/2 登录 → 解析 token/gate → Gate 发业务消息
用法:
  python tools/verify_login_flow.py <account> <password> [mac]
例:
  python tools/verify_login_flow.py wingzer test1234 54-05-DB-91-34-AB
"""
import base64
import hashlib
import random
import socket
import struct
import subprocess
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
    """返回 {field_no: (wire, value)} 简化解析"""
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


def get_mac():
    try:
        out = subprocess.check_output("getmac /fo csv /nh", shell=True).decode("gbk", "replace")
        for line in out.splitlines():
            parts = line.split(",")
            if len(parts) >= 2 and "-" in parts[1]:
                return parts[1].strip('"')
    except Exception:
        pass
    return "54-05-DB-91-34-AB"


def b64url(b):
    """Go base64.URLEncoding（- _），与客户端/服务器一致"""
    return base64.urlsafe_b64encode(b).decode("ascii")


def build_login_body(account, pwd, mac):
    md5 = hashlib.md5(pwd.encode("utf-8")).hexdigest().upper()
    proto = b""
    proto += b"\x12" + bytes([len(account)]) + account.encode("ascii")       # f2 account
    proto += b"\x1a" + bytes([len(md5)]) + md5.encode("ascii")               # f3 md5
    proto += b"\x22" + bytes([len(mac)]) + mac.encode("ascii")               # f4 mac
    raknet = random.getrandbits(64) | 0x1000000000000000                     # f6 随机 uint64
    proto += b"\x30" + varint(raknet)
    body = b"\x00\x01" + proto
    return b64url(body)


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
    sid = conn.get_next_available_stream_id()
    conn.send_headers(sid, headers, end_stream=False)
    conn.send_data(sid, body_b64, end_stream=True)
    sock.sendall(conn.data_to_send())
    resp_headers = []
    resp_body = b""
    ended = False
    while not ended:
        data = sock.recv(65535)
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


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return
    account = sys.argv[1]
    pwd = sys.argv[2]
    mac = sys.argv[3] if len(sys.argv) > 3 else get_mac()

    print("== 1. HTTP/2 登录 %s:%d ==" % (LOGIN_HOST, LOGIN_PORT))
    print("   account=%s mac=%s" % (account, mac))
    body = build_login_body(account, pwd, mac)
    headers, resp = h2_request(LOGIN_HOST, LOGIN_PORT, "/login/", body)
    print("   status:", [v for k, v in headers if k == ":status"])
    try:
        s = resp.decode("ascii")
        dec = base64.urlsafe_b64decode(s + "=" * ((-len(s)) % 4))
    except Exception as e:
        print("   body parse fail: %s raw=%s" % (e, resp.hex()))
        return
    print("   body(%d B): %s" % (len(dec), dec.hex()))
    f = proto_parse(dec)
    ret = f.get(1)
    print("   retCode:", ret)
    if not ret or ret[1] != 1:
        err = f.get(2, ("bytes", b""))[1]
        print("   登录失败! errStr=%r" % err)
        return
    uid = f[3][1]
    token = f[4][1].decode("ascii")
    gate_raw = f[5][1] if 5 in f else b""
    gate_host = gate_port = None
    if gate_raw:
        gf = proto_parse(gate_raw)
        addr = gf.get(1, ("bytes", b""))[1].decode("ascii", "replace")
        if ":" in addr:
            gate_host, gate_port = addr.rsplit(":", 1)
            gate_port = int(gate_port)
    print("   uid  : %d" % uid)
    print("   token: %s" % token)
    print("   gate : %s:%s" % (gate_host, gate_port))
    if not token or not gate_host:
        print("   token/gate 缺失！")
        return

    print("\n== 2. Gate 连接 %s:%s POST /game/ ==" % (gate_host, gate_port))
    # MissionListCMsg(1172): proto{f1=uid, f2=token}
    proto2 = b"\x08" + varint(uid) + b"\x12" + bytes([len(token)]) + token.encode("ascii")
    body2 = b64url(struct.pack("<H", 1172) + proto2)
    h2, b2 = h2_request(gate_host, gate_port, "/game/", body2)
    print("   status:", [v for k, v in h2 if k == ":status"])
    try:
        s2 = b2.decode("ascii")
        dec2 = base64.urlsafe_b64decode(s2 + "=" * ((-len(s2)) % 4))
        print("   resp(%d B): %s" % (len(dec2), dec2.hex()))
        print("   --- 响应 protobuf ---")
        for fn, (wt, v) in proto_parse(dec2).items():
            if wt == "bytes" and all(32 <= b < 127 for b in v) and v:
                print("   f%d str=%r" % (fn, v.decode("ascii", "replace")))
            elif wt == "bytes":
                print("   f%d bytes(%d)=%s" % (fn, len(v), v.hex()))
            else:
                print("   f%d %s=%s" % (fn, wt, v))
    except Exception as e:
        print("   resp raw: %s (%s)" % (b2.hex(), e))


if __name__ == "__main__":
    main()
