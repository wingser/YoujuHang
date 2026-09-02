# -*- coding: utf-8 -*-
"""一键分析 h5_xzone.log 中所有 HTTP/2 会话（登录18000 + 网关），解码 HEADERS/DATA"""
import io
import re
import sys
import base64
sys.path.insert(0, r"d:\Git\YoujuHang\tools")
from proto_decode import parse_msg_str
try:
    from hpack import Decoder
    HAS_HPACK = True
except Exception:
    HAS_HPACK = False

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

LOG = r"C:\Users\Lenovo\AppData\Local\Temp\h5_xzone.log"

ID_MAP = {
    256: "LsLoginCMsg", 258: "LsLoginSMsg",
    512: "EnterLobbyCMsg", 513: "GameCfgQueryCMsg", 514: "UserInfoCMsg",
    515: "RefreshInfoCMsg", 549: "HeartBeatCMsg", 550: "HeartBeatSMsg",
    1172: "MissionListCMsg", 1174: "MissionSubmitCMsg",
    1175: "PlayerCheckInCMsg", 1184: "PlayerSramCMsg", 1186: "PlayerExpAddCMsg",
    2304: "GateLoginCMsg", 2305: "GateLoginSMsg",
}

def main():
    lines = io.open(LOG, encoding="utf-8", errors="replace").readlines()
    # 收集所有 H2 sock 的发送/接收事件
    events = []
    i = 0
    while i < len(lines):
        l = lines[i]
        m = re.search(r"\[(\d+)\] (SEND|RECV) sock=(\d+) tag=ws2_32.dll!(WSASend|WSARecv|send|recv) bufCount=1 WSABUF.len=(\d+)", l)
        if m:
            t = int(m.group(1))
            dr = m.group(2)
            sock = int(m.group(3))
            data_lines = []
            j = i + 1
            while j < len(lines) and not lines[j].startswith("["):
                data_lines.append(lines[j])
                j += 1
            raw = "".join(data_lines)
            idx = raw.find("...")
            if idx >= 0:
                raw = raw[:idx]
            try:
                b = bytes.fromhex(raw.replace(" ", "").replace("\n", ""))
            except Exception:
                b = None
            if b:
                events.append((t, dr, sock, b))
            i = j
        else:
            i += 1
    events.sort(key=lambda e: e[0])
    # 按 sock 分组解析 H2 帧
    socks = {}
    for t, dr, sock, buf in events:
        if sock in (3136, 2496):
            continue
        socks.setdefault(sock, []).append((t, dr, buf))
    print("H2 socks:", sorted(socks.keys()))
    for sock in sorted(socks.keys()):
        print("\n" + "#" * 70)
        print("sock=%d" % sock)
        dec = Decoder() if HAS_HPACK else None
        streams = {}
        for t, dr, buf in socks[sock]:
            off = 0
            while off + 9 <= len(buf):
                length = (buf[off] << 16) | (buf[off+1] << 8) | buf[off+2]
                ftype = buf[off+3]
                flags = buf[off+4]
                stream = (buf[off+5] << 24) | (buf[off+6] << 16) | (buf[off+7] << 8) | buf[off+8]
                body = buf[off+9:off+9+length]
                if stream == 0:
                    # 控制帧，跳过
                    pass
                elif off + 9 + length > len(buf):
                    # 残帧（分片未收全），跳过
                    pass
                elif ftype == 1:  # HEADERS
                    hs = []
                    if dec:
                        try:
                            hs = dec.decode(body)
                        except Exception as e:
                            hs = [("DECODE_ERR", str(e))]
                    streams.setdefault(stream, []).append(("H", t, dr, hs))
                elif ftype == 0 and stream & 1 == 1:
                    # DATA
                    try:
                        if body and all(0x20 <= c < 0x7f for c in body):
                            s = body.decode().replace("-", "+").replace("_", "/")
                            s += "=" * (-len(s) % 4)
                            body = base64.b64decode(s)
                    except Exception:
                        pass
                    streams.setdefault(stream, []).append(("D", t, dr, body))
                off += 9 + length
        for stream in sorted(streams.keys()):
            for kind, t, dr, obj in streams[stream]:
                if kind == "H":
                    print("\n  [%d] %s stream=%d HEADERS" % (t, dr, stream))
                    for k, v in obj:
                        print("      %s: %s" % (k, v))
                else:
                    print("  [%d] %s stream=%d DATA (%d bytes)" % (t, dr, stream, len(obj)))
                    if len(obj) >= 2:
                        mid = (obj[1] << 8) | obj[0]  # little-endian
                        print("      ID=%d (%s)" % (mid, ID_MAP.get(mid, "?")))
                        try:
                            print("      %s" % parse_msg_str(obj[2:]))
                        except Exception as e:
                            print("      parse err:", e)

if __name__ == "__main__":
    main()
