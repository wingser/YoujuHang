# -*- coding: utf-8 -*-
"""全量解析 h5_xzone.log：所有 sock 的 H2 帧 -> base64(补 padding) -> protobuf/HEX
用法: python tools/analyze_h5_full.py [log_path] [sock_filter]
"""
import io
import re
import sys
import base64

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

LOG = r"C:\Users\Lenovo\AppData\Local\Temp\h5_xzone.log"
SOCK_FILTER = None
if len(sys.argv) > 1:
    LOG = sys.argv[1]
if len(sys.argv) > 2:
    SOCK_FILTER = int(sys.argv[2])

ID_MAP = {
    256: "LsLoginCMsg", 257: "LsTrapperCMsg", 258: "LsSteamLoginCMsg",
    512: "EnterLobbyCMsg", 513: "GameCfgQueryCMsg", 514: "UserInfoCMsg",
    515: "RefreshInfoCMsg", 516: "QueryZoneInfoCMsg", 517: "EnterZoneCMsg",
    531: "PlayerInOutZoneNtf", 532: "PlayerInOutRoomNtf", 533: "KickNtf",
    534: "LeaveZoneCMsg", 549: "HeartBeatCMsg", 550: "HeartBeatSMsg",
    560: "QueryAllZoneListCMsg", 561: "ChatCMsg", 562: "ChatNtf",
    563: "ClientShowMsgNtf", 564: "RankListCMsg", 565: "GetMailListCMsg",
    566: "ReadMailCMsg", 567: "TakeMailAttachCMsg", 568: "DelMailCMsg",
    569: "MailNtf", 576: "UseItemCMsg", 577: "AnnounceNtf", 578: "RootUpdateNtf",
    581: "GamePlayerInfoCMsg", 582: "SystemConfigNtf", 583: "ReportPlayerCMsg",
    584: "UpdateLocalStatusCMsg", 585: "UpdateLocalStatusNtf",
    592: "InvitePlayerCMsg", 593: "InvitePlayerNtf", 594: "PlayerMutePlayerCMsg",
    595: "ChangeInfoNtf", 596: "GuildCMsg", 597: "GuildNtf",
    598: "UsePaidItemCMsg", 599: "TournamentGameCfgCMsg", 600: "VipCMsg",
    601: "VipNtf", 603: "PlayerAuthenticationNtf", 604: "FollowPlayerCMsg",
    605: "FollowPlayerNtf", 1024: "QueryZonePlayerListCMsg",
    1025: "QueryZoneRoomListCMsg", 1026: "EnterRoomCMsg", 1027: "LeaveRoomCMsg",
    1028: "UploadRoomReplayCMsg", 1029: "UploadRoomReplayNtf",
    1030: "UploadRoomSnapShotCMsg", 1031: "UploadRoomSnapShotNtf",
    1032: "GetRoomSnapShotReplayCMsg", 1033: "ChangeRoomSlotCMsg",
    1040: "ChangeRoomSlotNtf", 1041: "ChangeRoomGameStatusNtf",
    1042: "NetPlayingInfoCMsg", 1043: "NetPlayingInfoNtf", 1044: "CreateRoomNtf",
    1045: "CloseRoomNtf", 1046: "ChangeRoomSettingCMsg",
    1047: "ChangeRoomSettingNtf", 1048: "WinLossChangeNtf", 1049: "RingRoomCMsg",
    1056: "RingRoomNtf", 1057: "RoomTvgCMsg", 1058: "TournamentGameResultNtf",
    1059: "GetVerifyImgCMsg", 1060: "WinLossAndAchievementUploadCMsg",
    1061: "AchievementUploadNtf", 1062: "AchievementEscapeUploadNtf",
    1063: "AchievementStageStartTimeNtf", 1072: "QuickGameCMsg",
    1073: "QuickGameNtf", 1074: "RoomMuteCMsg", 1076: "PlayerKickPlayerCMsg",
    1078: "RoomInfoChangeNtf", 1080: "PresentGift2PlayerCMsg",
    1081: "PresentGift2PlayerNtf", 1082: "ChangeRoomSlotPlayerInfoNtf",
    1088: "InGameStatusChangeCMsg", 1089: "RoomVoteCMsg", 1090: "RoomVoteNtf",
    1091: "TournamentInfoChangeNtf", 1092: "GetRoomSramMemCardCMsg",
    1093: "UploadRoomSramMemCardCMsg", 1094: "UploadRoomSnapShotFailNtf",
    1095: "PlayerExtraInfoNtf", 1096: "RoomPlayNtf", 1097: "CheckRoomPassCMsg",
    1172: "MissionListCMsg", 1174: "MissionSubmitCMsg", 1175: "PlayerCheckInCMsg",
    1176: "PlayerSocialCMsg", 1178: "PlayerSocialNtf", 1184: "PlayerSramCMsg",
    1185: "RequestMakeRoomSramMemCardNtf", 1186: "PlayerExpAddCMsg",
    1187: "QueryGameCfgCMsg", 2304: "GmCommandCMsg", 2305: "GateLoginSMsg",
    2449: "GateForwardToPlayerMsg",
}

def hexs_to_bytes(hs):
    try:
        idx = hs.find("...")
        if idx >= 0:
            hs = hs[:idx]
        return bytes.fromhex(hs.replace(" ", "").replace("\n", ""))
    except Exception:
        return None

def b64_pad(b):
    """补 base64 padding 并解码（支持 URL-safe 字符 -_）"""
    if not b:
        return None
    try:
        s = b.decode("ascii")
    except Exception:
        return None
    # 允许标准 +/ 与 URL-safe -_ 混合；若含非法字符则放弃
    if not re.fullmatch(r"[A-Za-z0-9+/_-]*={0,2}", s):
        return None
    pad = (-len(s)) % 4
    if pad:
        s += "=" * pad
    s = s.replace("-", "+").replace("_", "/")
    try:
        return base64.b64decode(s)
    except Exception:
        return None

def parse_h2_frames(data):
    """解析可能的 H2 帧序列（尝试偏移 0..2）"""
    out = []
    for start in range(0, 3):
        frames = []
        off = start
        ok = True
        while off + 9 <= len(data):
            length = (data[off] << 16) | (data[off+1] << 8) | data[off+2]
            if length > 65535 or off + 9 + length > len(data):
                ok = False
                break
            ftype = data[off+3]
            flags = data[off+4]
            stream = (data[off+5] << 24) | (data[off+6] << 16) | (data[off+7] << 8) | data[off+8]
            body = data[off+9:off+9+length]
            frames.append({"type": ftype, "flags": flags, "stream": stream, "body": body})
            off += 9 + length
        if ok and frames and off == len(data):
            out = frames
            break
    return out

FTYPES = {0: "DATA", 1: "HEADERS", 2: "PRIORITY", 3: "RST_STREAM", 4: "SETTINGS",
          5: "PUSH_PROMISE", 6: "PING", 7: "GOAWAY", 8: "WINDOW_UPDATE", 9: "CONTINUATION"}

def parse_varint(b, i):
    result = 0
    shift = 0
    while True:
        byte = b[i]
        i += 1
        result |= (byte & 0x7f) << shift
        if not (byte & 0x80):
            break
        shift += 7
    return result, i

def decode_protobuf(b):
    fields = []
    i = 0
    try:
        while i < len(b):
            tag, i = parse_varint(b, i)
            field_num = tag >> 3
            wire = tag & 7
            if wire == 0:
                val, i = parse_varint(b, i)
                fields.append((field_num, "varint", val))
            elif wire == 2:
                length, i = parse_varint(b, i)
                val = b[i:i+length]
                i += length
                fields.append((field_num, "bytes", val))
            elif wire == 5:
                val = int.from_bytes(b[i:i+4], "little")
                i += 4
                fields.append((field_num, "fixed32", val))
            elif wire == 1:
                val = int.from_bytes(b[i:i+8], "little")
                i += 8
                fields.append((field_num, "fixed64", val))
            else:
                fields.append((field_num, "wire%d" % wire, b[i:]))
                break
    except Exception:
        pass
    return fields

def fmt_field(f):
    fn, wt, val = f
    if wt == "bytes":
        try:
            s = val.decode("ascii")
            if all(32 <= ord(c) < 127 for c in s):
                return "f%d=%r" % (fn, s)
        except Exception:
            pass
        if len(val) <= 16:
            return "f%d=<%s>" % (fn, val.hex())
        return "f%d=<%d bytes>" % (fn, len(val))
    return "f%d=%s" % (fn, val)

def main():
    lines = io.open(LOG, encoding="utf-8", errors="replace").readlines()
    events = []
    i = 0
    while i < len(lines):
        l = lines[i]
        m = re.search(r"\[(\d+)\] (SEND|RECV) sock=(\d+)", l)
        if m:
            t = int(m.group(1))
            dr = m.group(2)
            sock = int(m.group(3))
            data_lines = []
            j = i + 1
            while j < len(lines) and not lines[j].startswith("["):
                data_lines.append(lines[j])
                j += 1
            b = hexs_to_bytes("".join(data_lines))
            if b and len(b) > 0:
                events.append((t, dr, sock, b))
            i = j
        else:
            i += 1

    events.sort(key=lambda e: e[0])
    for t, dr, sock, b in events:
        if SOCK_FILTER is not None and sock != SOCK_FILTER:
            continue
        frames = parse_h2_frames(b)
        if not frames:
            continue
        for fr in frames:
            tname = FTYPES.get(fr["type"], "?%d" % fr["type"])
            body = fr["body"]
            if tname == "DATA":
                dec = b64_pad(body)
                if dec is not None:
                    show = dec.hex()
                    proto = ""
                    fields = decode_protobuf(dec)
                    if fields:
                        proto = "  proto: " + ", ".join(fmt_field(f) for f in fields)
                    print("[%d] %s sock=%d stream=%d DATA(b64) len=%d %s%s" % (t, dr, sock, fr["stream"], len(dec), show, proto))
                else:
                    # 可能直接是 protobuf（非 base64）
                    fields = decode_protobuf(body)
                    if fields and all(f[0] < 30 for f in fields):
                        print("[%d] %s sock=%d stream=%d DATA(raw) len=%d %s  proto: %s" % (
                            t, dr, sock, fr["stream"], len(body), body.hex(),
                            ", ".join(fmt_field(f) for f in fields)))
                    else:
                        print("[%d] %s sock=%d stream=%d DATA(len=%d) %s" % (t, dr, sock, fr["stream"], len(body), body.hex()))
            elif tname == "HEADERS":
                print("[%d] %s sock=%d stream=%d HEADERS flags=%02x hpack=%s" % (t, dr, sock, fr["stream"], fr["flags"], body.hex()))
            elif tname in ("SETTINGS", "PING", "WINDOW_UPDATE"):
                print("[%d] %s sock=%d stream=%d %s %s" % (t, dr, sock, fr["stream"], tname, body.hex()))

if __name__ == "__main__":
    main()
