# -*- coding: utf-8 -*-
"""从 h5_xzone.log 提取 H2 DATA 帧（base64 解码 -> 前缀+protobuf），解码显示"""
import io
import re
import sys
import base64
sys.path.insert(0, r"d:\Git\YoujuHang\tools")
from proto_decode import parse_msg_str

try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

LOG = r"C:\Users\Lenovo\AppData\Local\Temp\h5_xzone.log"

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

def try_b64(b):
    """如果 b 全 ASCII 可打印则尝试 base64 解码"""
    if not b:
        return None
    try:
        if all(0x20 <= c < 0x7f for c in b):
            return base64.b64decode(b)
    except Exception:
        return None
    return None

def main():
    lines = io.open(LOG, encoding="utf-8", errors="replace").readlines()
    events = []
    i = 0
    while i < len(lines):
        l = lines[i]
        m = re.search(r"\[(\d+)\] (SEND|RECV) sock=(\d+) tag=ws2_32.dll!(WSASend|WSARecv|send|recv) bufCount=1 WSABUF.len=(\d+)", l)
        if m:
            t = int(m.group(1))
            dr = m.group(2)
            sock = int(m.group(3))
            if sock not in (3136, 2496):
                data_lines = []
                j = i + 1
                while j < len(lines) and not lines[j].startswith("["):
                    data_lines.append(lines[j])
                    j += 1
                buf = hexs_to_bytes("".join(data_lines))
                if buf:
                    off = 0
                    while off + 9 <= len(buf):
                        length = (buf[off] << 16) | (buf[off+1] << 8) | buf[off+2]
                        ftype = buf[off+3]
                        flags = buf[off+4]
                        stream = (buf[off+5] << 24) | (buf[off+6] << 16) | (buf[off+7] << 8) | buf[off+8]
                        body = buf[off+9:off+9+length]
                        if ftype == 0 and stream & 1 == 1:  # DATA 客户端 stream
                            dec = try_b64(body)
                            if dec is None:
                                dec = body
                            events.append((t, dr, sock, stream, dec))
                        off += 9 + length
                i = j
            else:
                i += 1
        else:
            i += 1

    events.sort(key=lambda e: e[0])
    for t, dr, sock, stream, body in events:
        print("\n[%d] %s sock=%d stream=%d DATA len=%d" % (t, dr, sock, stream, len(body)))
        print("  raw: %s" % body.hex())
        if len(body) >= 2:
            mid = (body[0] << 8) | body[1]
            print("  prefix[0:2]=%02x%02x big-endian=%d %s" % (body[0], body[1], mid, ID_MAP.get(mid, "?")))
        # varint 前缀
        v = 0
        shift = 0
        for k, b in enumerate(body[:4]):
            v |= (b & 0x7f) << shift
            shift += 7
            if not (b & 0x80):
                print("  prefix[0] varint=%d %s" % (v, ID_MAP.get(v, "?")))
                break
        try:
            print("  proto[2:]: %s" % parse_msg_str(body[2:]))
        except Exception as e:
            print("  proto err:", e)

if __name__ == "__main__":
    main()
