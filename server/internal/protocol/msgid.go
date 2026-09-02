// Package protocol 定义消息 ID 常量与业务消息构造（与 docs/protocol_notes.md 一致）
package protocol

import (
	"youjuhang/internal/proto"
)

// 消息 ID（来自逆向实测，见 docs/protocol_notes.md）
const (
	LsLoginCMsg       uint16 = 256  // LS 登录请求
	LsLoginSMsg       uint16 = 258  // LS 登录响应 ret/uid/token/game
	EnterLobbyCMsg    uint16 = 512  // 进大厅 uid/gameToken/gameVersion（房间消息的前置必需步骤，见 BuildEnterLobby）
	LeaveZoneCMsg     uint16 = 534  // 离开区/登出：清空本 uid 的登录会话。exe 退出时主动释放，避免游聚残留僵尸 session
	UserInfoCMsg      uint16 = 514  // 用户信息
	RefreshInfoCMsg   uint16 = 515  // 刷新信息 uid/gameToken/action
	HeartBeatCMsg     uint16 = 549  // 心跳
	HeartBeatSMsg     uint16 = 550  // 心跳响应
	MissionListCMsg   uint16 = 1172 // 任务列表
	MissionSubmitCMsg uint16 = 1174 // 任务提交（领奖/上报）
	PlayerCheckInCMsg uint16 = 1175 // 每日签到
	TeamContributeCMsg uint16 = 596  // 战队操作（f3=15 贡献捐献；f3=3 战队详情查询）

	// 进区握手消息：走 POST /game/，不是 /zone/2/！
	// 这是 2026-08-28 修复的核心 BUG：此前 516/517 一律发到 /zone/2/，zone 处理器不认识
	// 这两个消息号，恒定返回 ret=-14 "UNHANDLED_MSG_ID zone 0x204 516 / 0x205 517"，
	// 导致数日无法进区、建房/观战从未发生。
	// 抓包证据：hang_spectate_20260827.pcapng（一次完整成功的建房挂机会话）中
	// 516/517 的路径是 /game/，随后的 1024/1025/1026/1028/1042/1092 才是 /zone/2/。
	QueryZoneInfoCMsg uint16 = 516 // /game/ 查询区信息
	EnterZoneCMsg     uint16 = 517 // /game/ 进入分区

	// 分区/房间消息（发送路径 POST /zone/2/，与 /game/ 同一条 HTTP/2 连接、同一 gate 地址）
	//
	// 更正（2026-08-29 实测）：1025 的响应**不是**强加密数据。
	// 请求体只带 f1=uid f2=token（无额外参数）时，1025 返回明文 protobuf RoomInfo 列表
	// （实测本区 187 个房间，f3 重复出现，含房间号/房主 uid/房间名/观战设置），
	// 可直接解析，见 room.QueryRooms。此前「强加密、Go 端无法解析」的判断导致长期
	// 只能随机盲猜房间号，是建房失败迟迟无法定位的重要原因。
	// 1024 同样可解析（区玩家列表）。
	QueryZonePlayerListCMsg   uint16 = 1024 // 区玩家列表（明文 protobuf）
	QueryZoneRoomListCMsg     uint16 = 1025 // 区房间列表（明文 protobuf，可解析出全部房间号）
	EnterRoomCMsg             uint16 = 1026 // 进房/建房（f3=roomId f4=isCreate f5=RoomCfgInfo f8=showVipMsg）
	LeaveRoomCMsg             uint16 = 1027 // 离房（f1=uid f2=token）
	RoomHeartBeatCMsg         uint16 = 1028 // 房间心跳（仅房主/建房者，约每秒 1 次）
	GetRoomSnapShotReplayCMsg uint16 = 1032 // 房间快照轮询（f3=1 f6=seq）
	ChangeRoomSlotCMsg        uint16 = 1033 // 上位/换座（f3=slot，实测上位用 1）
	PresentGift2PlayerCMsg    uint16 = 1080 // 进房后自动送礼（f3=action）
	RoomStatusCMsg            uint16 = 1092 // 房间状态轮询（f3=seq，约每 1.6 秒 1 次）
	RoomOnlineReportCMsg      uint16 = 1042 // 在线时长/贡献上报（房主身份，与心跳同步，约每秒 1 次；f3={f1=3, f2=递增tick}）
)

// GameBody 构造网关请求 body：base64url([2B MsgID LE][protobuf])
func GameBody(msgID uint16, protoFields []byte) []byte {
	b := make([]byte, 2, 2+len(protoFields))
	b[0] = byte(msgID)
	b[1] = byte(msgID >> 8)
	return append(b, protoFields...)
}

// FieldsWithAuth 构造带 uid/token 的基础字段
func FieldsWithAuth(uid uint32, token string) []byte {
	b := proto.FieldVarint(nil, 1, uint64(uid))
	b = proto.FieldString(b, 2, token)
	return b
}

// BuildLoginProto 构造 LsLoginCMsg protobuf（不含 [00 01] 前缀）
// 实测只发送 f2=account / f3=MD5(密码).upper() / f4=mac / f6=随机uint64
func BuildLoginProto(account, pwdHash, mac string, raknetID uint64) []byte {
	b := proto.FieldString(nil, 2, account)
	b = proto.FieldString(b, 3, pwdHash)
	b = proto.FieldString(b, 4, mac)
	b = proto.FieldVarint(b, 6, raknetID)
	return b
}

// BuildEnterLobby 构造 EnterLobbyCMsg(512)。
//
// 关键：这条消息是登录 token 的「激活」步骤，房间挂机能否工作完全依赖它。
// 实测（2026-08-29 对照实验，同一账号同一房间号 43）：
//   - 跳过 512 → 1026 返回 ret=-10 "WRONG_GAME_TOKEN"，建房/进房/观战全部失败
//   - 发送 512 → 1026 返回 ret=1，建房/进房/观战/上位全部正常
//
// 迷惑点：/game/ 侧的消息（516 查询区、517 进区、任务、战队、签到）不发送 512 也能
// 正常响应，会让人误以为 token 没问题；只有 /zone/2/ 的房间消息会校验 token 激活状态。
// 因此 session.Login 在登录成功后必须调用本消息，否则房间挂机会静默失效。
func BuildEnterLobby(uid uint32, token string) []byte {
	return FieldsWithAuth(uid, token)
}

// BuildRefreshInfo 构造 RefreshInfoCMsg(515, action)
func BuildRefreshInfo(uid uint32, token string, action uint32) []byte {
	b := FieldsWithAuth(uid, token)
	b = proto.FieldVarint(b, 3, uint64(action))
	return b
}

// BuildMissionList 构造 MissionListCMsg(1172)
func BuildMissionList(uid uint32, token string) []byte {
	return FieldsWithAuth(uid, token)
}

// BuildMissionSubmit 构造 MissionSubmitCMsg(1174)
// missionIDs: 领取的任务 ID 列表；day: 当日日期（dailyOnline.day）
func BuildMissionSubmit(uid uint32, token string, missionIDs []uint64, day int32) []byte {
	b := FieldsWithAuth(uid, token)
	// f3 = missionIdList (bytes)，内容为任务 ID 的 varint 序列
	var ids []byte
	for _, id := range missionIDs {
		ids = proto.AppendVarint(ids, id)
	}
	b = proto.FieldBytes(b, 3, ids)
	// f5 = dailyOnline{day=当日}
	var d []byte
	d = proto.FieldVarint(d, 1, uint64(day))
	b = proto.FieldBytes(b, 5, d)
	return b
}

// BuildCheckIn 构造 PlayerCheckInCMsg(1175, checkInType)
// 实测 checkInType=2 为每日签到
func BuildCheckIn(uid uint32, token string, checkInType uint32) []byte {
	b := FieldsWithAuth(uid, token)
	b = proto.FieldVarint(b, 3, uint64(checkInType))
	return b
}

// 战队操作动作值（TeamContributeCMsg f3，实测）
const (
	TeamActionInfo      uint32 = 3  // 战队详情查询（响应 base64+zlib 大包）
	TeamActionContribute uint32 = 15 // 贡献捐献
)

// BuildTeamContribute 构造 TeamContributeCMsg(596) 贡献捐献请求
// amount: 捐献贡献度数量（实测 500/1000）
// 实测请求：f1=uid f2=token f3=15 f9={f1=amount}
// 响应：f1=ret(1=成功) f8={f1=捐前余额 f2=捐后余额 f3=? f4=?}
func BuildTeamContribute(uid uint32, token string, amount uint32) []byte {
	b := FieldsWithAuth(uid, token)
	b = proto.FieldVarint(b, 3, uint64(TeamActionContribute))
	var sub []byte
	sub = proto.FieldVarint(sub, 1, uint64(amount))
	return proto.FieldBytes(b, 9, sub)
}

// BuildTeamInfo 构造 TeamContributeCMsg(596) 战队详情查询请求（f3=3）
// 实测响应 = [0x2B 前缀] base64url(zlib(pb))；pb: f1=ret f6={f4=uid f5=贡献度余额 ...}
func BuildTeamInfo(uid uint32, token string) []byte {
	b := FieldsWithAuth(uid, token)
	return proto.FieldVarint(b, 3, uint64(TeamActionInfo))
}

// ZoneHandshakeVer 进区握手固定版本号（516/517 的 f4）。
// 抓包实测客户端恒定发送 1582，且 516 响应 f3.f2 原样回显该值。
const ZoneHandshakeVer = 1582

// EnterZoneP2PAddr / EnterZoneP2PPort 为 517 的 f6/f7。
// 抓包实测客户端恒定发送 22028204 / 50296（疑为客户端 P2P 端点，区配置 P2p=0 时服务端忽略），
// 逐字复现已验证成功。
const (
	EnterZoneP2PAddr = 22028204
	EnterZoneP2PPort = 50296
)

// BuildZoneInfo 构造 QueryZoneInfoCMsg(516)：进区前查询区信息（POST /game/）
// 实测请求：f1=uid f2=token f3=3 f4=1582
// 响应：f3={f1=3 f2=1582(回显) f3=1 f4=265}
func BuildZoneInfo(uid uint32, token string) []byte {
	b := FieldsWithAuth(uid, token)
	b = proto.FieldVarint(b, 3, 3)
	return proto.FieldVarint(b, 4, ZoneHandshakeVer)
}

// BuildEnterZone 构造 EnterZoneCMsg(517)：进入分区（POST /game/）
// 实测请求：f1=uid f2=token f3=3 f4=1582 f5=zoneId f6=22028204 f7=50296
// 响应：f1=ret(1=成功) f3=对局服务器地址("ip:port") f9=区配置 JSON（含 fc_8bit 等版本列表）
func BuildEnterZone(uid uint32, token string, zoneID uint32) []byte {
	b := FieldsWithAuth(uid, token)
	b = proto.FieldVarint(b, 3, 3)
	b = proto.FieldVarint(b, 4, ZoneHandshakeVer)
	b = proto.FieldVarint(b, 5, uint64(zoneID))
	b = proto.FieldVarint(b, 6, EnterZoneP2PAddr)
	return proto.FieldVarint(b, 7, EnterZoneP2PPort)
}

// BuildEnterRoom 构造 EnterRoomCMsg(1026)：进入已有房间（观战/加入）
// 实测请求：f1=uid f2=token f3=roomId f8=1（抓包 roomId=46，响应 f3=slot，观战=255）
func BuildEnterRoom(uid uint32, token string, roomID int32) []byte {
	b := FieldsWithAuth(uid, token)
	b = proto.FieldVarint(b, 3, uint64(int64(roomID)))
	return proto.FieldVarint(b, 8, 1) // showVipMsg=true
}

// BuildChangeRoomSlot 构造 ChangeRoomSlotCMsg(1033)：观战上位/换座
// slot: 目标座位号（实测客户端点击"加入游戏上位"发送 f3=1）
func BuildChangeRoomSlot(uid uint32, token string, slot int32) []byte {
	b := FieldsWithAuth(uid, token)
	return proto.FieldVarint(b, 3, uint64(int64(slot)))
}

// 建房房间配置固定参数（实测客户端数值）
const (
	HangGameVersion uint32 = 637665792 // 客户端版本号（实测固定值）
	HangVersionID   uint64 = 1         // fc_8bit 版本 ID
	HangObStatus    uint64 = 2         // 观战设置（实测值）
)

// BuildCreateRoom 构造 EnterRoomCMsg(1026)：建房
// roomID: 期望房间号（客户端自行选号）；cfg: BuildRoomCfg 生成的房间配置
func BuildCreateRoom(uid uint32, token string, roomID int32, cfg []byte) []byte {
	b := BuildEnterRoom(uid, token, roomID)
	b = proto.FieldVarint(b, 4, 1)    // isCreate=true
	b = proto.FieldBytes(b, 5, cfg)   // RoomCfgInfo
	return proto.FieldVarint(b, 8, 1) // showVipMsg=true
}

// BuildRoomCfg 构造建房房间配置 RoomCfgInfo
// roomName: 房间名；versionName: 游戏版本名（如 fc_8bit）；rom1MD5: ROM 的 MD5（如 fc_8bit 的 dc06...）
func BuildRoomCfg(roomName, versionName, rom1MD5 string) []byte {
	var b []byte
	b = proto.FieldString(b, 1, roomName)                 // roomName
	b = proto.FieldVarint(b, 5, HangVersionID)            // versionId=1（fc_8bit）
	b = proto.FieldVarint(b, 10, 1)                       // vipBst
	b = proto.FieldVarint(b, 12, HangObStatus)            // obStatus
	b = proto.FieldString(b, 15, rom1MD5)                 // rom1Md5
	b = proto.FieldVarint(b, 18, uint64(HangGameVersion)) // gameVersion
	b = proto.FieldString(b, 20, versionName)             // versionName
	return proto.FieldVarint(b, 24, 1)                    // topRoomDay
}

// BuildLeaveRoom 构造 LeaveRoomCMsg(1027)
func BuildLeaveRoom(uid uint32, token string) []byte {
	return FieldsWithAuth(uid, token)
}

// BuildRoomHeartBeat 构造 RoomHeartBeatCMsg(1028)
// seq 从 1 起递增；f3={f1={f1=seq,f2=60,f3=seq*60+1,f4=2,f5=60}, f2=20B空闲状态}
func BuildRoomHeartBeat(uid uint32, token string, seq uint32) []byte {
	b := FieldsWithAuth(uid, token)
	var inner []byte
	inner = proto.FieldVarint(inner, 1, uint64(seq))
	inner = proto.FieldVarint(inner, 2, 60)
	inner = proto.FieldVarint(inner, 3, uint64(seq)*60+1)
	inner = proto.FieldVarint(inner, 4, 2)
	inner = proto.FieldVarint(inner, 5, 60)
	var f3 []byte
	f3 = proto.FieldBytes(f3, 1, inner)
	// f2: 20 字节空闲状态数据（实测挂机时固定值）
	state := []byte{
		0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00, 0x00,
		0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff, 0xff,
		0x00, 0x00, 0x00, 0xe0,
	}
	f3 = proto.FieldBytes(f3, 2, state)
	b = proto.FieldBytes(b, 3, f3)
	return b
}

// BuildRoomStatus 构造 RoomStatusCMsg(1092)
// seq 为递增轮询序号（从 1 起）
func BuildRoomStatus(uid uint32, token string, seq uint32) []byte {
	b := FieldsWithAuth(uid, token)
	b = proto.FieldVarint(b, 3, uint64(seq))
	return b
}

// BuildSnapshot 构造 GetRoomSnapShotReplayCMsg(1032) 观战快照轮询
// 观战身份无 1028 心跳，靠快照轮询保活；f3=getSnapshot=1 f6=entityId(递增 seq)
func BuildSnapshot(uid uint32, token string, seq uint32) []byte {
	b := FieldsWithAuth(uid, token)
	b = proto.FieldVarint(b, 3, 1)
	return proto.FieldVarint(b, 6, uint64(seq))
}

// BuildOnlineReport 构造 RoomOnlineReportCMsg(1042)：在线时长/贡献度上报
// 房主（建房者/上位成功）身份约每秒发送一次，与 1028 心跳同步。
// 实测结构：f1=uid f2=token f3={inner: f1=3(固定), f2=tick(递增计数器)}
// tick 每帧递增（真实约 +52~60），服务器据此累计房主在线时长用于战队任务。
// 初值取较大常数以匹配真实客户端量级（服务器仅校验递增与频率，不校验绝对时间）。
func BuildOnlineReport(uid uint32, token string, tick uint32) []byte {
	b := FieldsWithAuth(uid, token)
	var inner []byte
	inner = proto.FieldVarint(inner, 1, 3)
	inner = proto.FieldVarint(inner, 2, uint64(tick))
	return proto.FieldBytes(b, 3, inner)
}
