# -*- coding: utf-8 -*-
"""复现游聚 HTTP/2 游戏网关协议：POST /game/，body=base64url([2B MsgID LE][protobuf])
用法:
  python tools/verify_gate.py gate_host gate_port uid token [msgid] [proto_hex]
例:
  python tools/verify_gate.py 153.99.234.163 18031 5571561 b61af... 1172 08e987d402
"""
import base64
import socket
import sys

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

from h2.config import H2Configuration
from h2.connection import H2Connection
from h2.events import DataReceived, ResponseReceived, StreamEnded

# MsgID 常量（LE 字节）
MSG_IDS = {
    256: "LsLoginCMsg", 512: "EnterLobbyCMsg", 514: "UserInfoCMsg",
    515: "RefreshInfoCMsg", 1172: "MissionListCMsg", 1174: "MissionSubmitCMsg",
    1175: "PlayerCheckInCMsg",
}


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


def build_msg(uid, token, msgid, extra_hex=""):
    """构造 [2B MsgID LE][protobuf{uid, gameToken, ...}]"""
    mid = int(msgid).to_bytes(2, "little")
    proto = b"\x08" + varint(uid)
    proto += b"\x12" + bytes([len(token)]) + token.encode("ascii")
    if extra_hex:
        proto += bytes.fromhex(extra_hex)
    return mid + proto


def b64url(b):
    return base64.b64encode(b).decode("ascii")


def main():
    if len(sys.argv) < 5:
        print(__doc__)
        return
    host = sys.argv[1]
    port = int(sys.argv[2])
    uid = int(sys.argv[3])
    token = sys.argv[4]
    msgid = int(sys.argv[5]) if len(sys.argv) > 5 else 1172
    extra = sys.argv[6] if len(sys.argv) > 6 else ""

    body = build_msg(uid, token, msgid, extra)
    payload = b64url(body)
    name = MSG_IDS.get(msgid, str(msgid))
    print("== 发送 %s(%d) 到 %s:%d ==" % (name, msgid, host, port))
    print("  proto: %s" % body.hex())
    print("  b64:   %s" % payload)

    sock = socket.create_connection((host, port), timeout=10)
    config = H2Configuration(client_side=True, header_encoding="utf-8")
    conn = H2Connection(config=config)
    conn.initiate_connection()
    sock.sendall(conn.data_to_send())

    headers = [
        (":method", "POST"),
        (":scheme", "http"),
        (":path", "/game/"),
        (":authority", "%s:%d" % (host, port)),
        ("content-type", "application/x-www-form-urlencoded"),
        ("content-length", str(len(payload))),
        ("user-agent", "Go-http-client/2.0"),
    ]
    stream_id = conn.get_next_available_stream_id()
    conn.send_headers(stream_id, headers, end_stream=False)
    conn.send_data(stream_id, payload.encode("ascii"), end_stream=True)
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

    print("\n== 响应 ==")
    for k, v in resp_headers:
        print("  %s: %s" % (k, v))
    try:
        s = resp_body.decode("ascii")
        dec = base64.b64decode(s + "=" * ((-len(s)) % 4), altchars=b"-_")
        print("  body(b64 %d B) → 解码 %d B: %s" % (len(s), len(dec), dec.hex()))
    except Exception as e:
        print("  body(hex): %s (%s)" % (resp_body.hex(), e))


if __name__ == "__main__":
    main()
