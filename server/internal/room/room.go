// Package room 实现分区/房间挂机：进区、建房、心跳、状态轮询、离房、上位
// 协议来源：captures/h2_stream20.txt（516/517 进区）、captures/h2_stream50.txt（建房/心跳/轮询/离房）、
// captures/hang_spectate_20260827.pcapng（进房/观战响应）、
// captures/join3_20260827_00001_20260827152918.pcapng（观战→1033 上位→恢复心跳）
package room

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"sort"
	"time"

	"youjuhang/internal/gate"
	"youjuhang/internal/proto"
	"youjuhang/internal/protocol"
)

// HangZoneID 挂机区 ID，写死为 1（"自由区"：517 响应 f9 Name="自由区"，VersionList 含 fc_8bit）。
// 这是唯一的音乐挂机区，不提供配置开关。
// 注意：请求路径 /zone/2/ 里的 2 是协议版本号，不是区 ID；区 ID 由 517 的 f5 单独指定。
// 历史坑：曾把路径里的 2 误当作区 ID 写进配置，服务器返回 ret=-18 "ZONE_NOT_FOUND 3 1582 2"。
const HangZoneID uint32 = 1

// 挂机区建房默认参数（实测）
const (
	HangRoomName    = "大家一起玩"                            // 默认房间名
	HangVersionName = "fc_8bit"                          // 建房使用的游戏版本
	HangRom1MD5     = "dc06babfb280c32ae895a020c86b69fb" // fc_8bit Rom1Md5（抓包实测）
	HangRetryAfter  = time.Hour                          // 进区/拉房间列表等基础设施失败后的重试间隔
	// HangPickRetry 建房与进房都失败后的重试间隔。
	// 这不是基础设施故障，只是候选房间号被别人抢先占用，很快就会空出来，
	// 因此不必等 1 小时（历史实现在盲猜失败后等 1 小时，白白浪费一整轮）。
	HangPickRetry = 30 * time.Second
	// hangCreateTries 单次挂机中尝试的候选房间号个数（空闲号与已占用号各取这么多个）
	hangCreateTries = 3

	// 建房失败进入他人房间后的上位流程参数（抓包实测）
	HangSpectateBefore = 15 * time.Second // 观战停留时间（模拟真实玩家再上位）
	HangSeatSlot       = 1                // 上位座位号（客户端点击"加入游戏上位"实测 f3=1）
	// 上位失败后保持观战挂机：观战身份无 1028 心跳，靠 1032 快照轮询保活
)

// zoneFallback 进区回退顺序：HangZoneID 优先，失败后依次尝试这里的区 ID。
// 背景：2026-08-28 19:18 起账号配置的 room_hang_zone=2，而区 2 在分配的 gate 上不存在，
// 服务器返回 ret=-18 "ZONE_NOT_FOUND 3 1582 2"。此前 Hang 只在单个区 ID 上死等重试，
// 导致连续约 22 小时无法进区、房间挂机任务完全无进展（该配置项现已删除，区 ID 写死为 1）。
// 保留回退作为保险：若将来服务器下架区 1，仍能自动尝试 2、3 而不是整天卡死。
// 实测 zone 1=自由区（含 fc_8bit）可用，故置于回退首位。
var zoneFallback = []uint32{1, 2, 3}

// VersionInfo 是 f9 分区 JSON 中 VersionList 的一项（一个游戏版本/ROM）
type VersionInfo struct {
	ID     uint32
	Name   string // 例如 fc_8bit
	Title  string // 例如 演奏起来
	Rom1   string
	Rom1MD string
}

// ZoneInfo 是分区配置（517 响应 f9 JSON 解析）
type ZoneInfo struct {
	ID      uint32 // 区 ID（自由区=1）
	Name    string // 区名（自由区）
	Battle  string // 对局服务器地址（517 响应 f3，形如 "ip:port"，目前仅供日志诊断）
	Version []VersionInfo
}

// jsonZone 对应 f9 分区 JSON（单个对象）
type jsonZone struct {
	ID          uint32        `json:"Id"`
	Name        string        `json:"Name"`
	VersionList []jsonVersion `json:"VersionList"`
}

type jsonVersion struct {
	ID      uint32 `json:"Id"`
	Name    string `json:"Name"`
	Title   string `json:"Title"`
	Rom1    string `json:"Rom1"`
	Rom1Md5 string `json:"Rom1Md5"`
}

// Client 是房间挂机客户端
type Client struct {
	gate *gate.Client
	// log 为注入的日志器（写入账号日志文件）。
	// 注意：本包原使用全局 slog（输出到 stderr），GUI 模式(-H=windowsgui)下 stderr 被丢弃，
	// 导致建房/进区/挂机等关键日志全部丢失、无法诊断。现统一通过注入的 logger 输出。
	log *slog.Logger
}

// New 创建房间挂机客户端
func New(g *gate.Client, log *slog.Logger) *Client {
	if log == nil {
		log = slog.Default()
	}
	return &Client{gate: g, log: log}
}

// parseZoneResp 解析 /zone/2/ 响应体。
// 实测 2026-08-26 之后 /zone/2/ 响应为纯 protobuf（无 MsgID 前缀、非 zlib），
// gate.Zone 已经做过 base64 解码与 zlib 解压，这里原样返回即可。
// （老协议版本响应曾带 2 字节 MsgID 前缀，现已废弃。）
func parseZoneResp(resp []byte) ([]byte, error) {
	if len(resp) < 2 {
		return nil, errors.New("room: short zone resp")
	}
	return resp, nil
}

// EnterZone 进入分区握手：516 查询 + 517 进区，两步都走 POST /game/（见 protocol.EnterZoneCMsg 注释）。
// 517 响应 f1=ret、f3=对局服务器地址、f9=区配置 JSON（含建房所需的 fc_8bit 版本信息）。
// 注意：/zone/2/ 只负责进区之后的房间消息（1024/1025/1026/1028/1042/1092），
// 516/517 发到 /zone/2/ 会被拒（UNHANDLED_MSG_ID）。
func (c *Client) EnterZone(ctx context.Context, addr string, uid uint32, token string, zoneID uint32) (zi *ZoneInfo, rerr error) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("room: EnterZone PANIC", "recovered", r, "zoneID", zoneID)
			rerr = fmt.Errorf("room: enter zone panic: %v", r)
		}
	}()
	c.log.Info("room: EnterZone start", "addr", addr, "uid", uid, "zoneID", zoneID)

	// 第一步：QueryZoneInfo(516) 走 /game/
	if _, err := c.gate.Game(ctx, addr, protocol.QueryZoneInfoCMsg, protocol.BuildZoneInfo(uid, token)); err != nil {
		c.log.Warn("room: EnterZone query(516) failed", "err", err)
		return nil, fmt.Errorf("room: query zone info: %w", err)
	}
	c.log.Info("room: EnterZone query(516) ok")

	// 第二步：EnterZone(517) 走 /game/，返回区配置
	resp, err := c.gate.Game(ctx, addr, protocol.EnterZoneCMsg, protocol.BuildEnterZone(uid, token, zoneID))
	if err != nil {
		c.log.Warn("room: EnterZone enter(517) failed", "err", err)
		return nil, fmt.Errorf("room: enter zone: %w", err)
	}
	c.log.Info("room: EnterZone enter(517) http ok", "respLen", len(resp))
	body, err := parseZoneResp(resp)
	if err != nil {
		return nil, err
	}
	f := proto.Parse(body)
	if ret := int64(proto.GetVarint(f, 1)); ret != 1 {
		c.log.Warn("room: EnterZone enter(517) ret != 1", "ret", ret, "errMsg", proto.GetString(f, 2), "bodyLen", len(body))
		return nil, fmt.Errorf("room: enter zone ret=%d err=%q", ret, proto.GetString(f, 2))
	}
	zi = &ZoneInfo{Battle: proto.GetString(f, 3)}
	if cfg := proto.GetString(f, 9); cfg != "" {
		var jz jsonZone
		if err := json.Unmarshal([]byte(cfg), &jz); err != nil {
			c.log.Warn("room: bad zone json", "err", err)
		} else {
			zi.ID, zi.Name = jz.ID, jz.Name
			for _, jv := range jz.VersionList {
				zi.Version = append(zi.Version, VersionInfo{
					ID: jv.ID, Name: jv.Name, Title: jv.Title, Rom1: jv.Rom1, Rom1MD: jv.Rom1Md5,
				})
			}
		}
	}
	c.log.Info("room: entered zone", "zoneID", zoneID, "name", zi.Name,
		"battle", zi.Battle, "versions", len(zi.Version))
	return zi, nil
}

// FindVersion 在分区配置中查找指定游戏版本（如 fc_8bit），找不到返回 false
func (z *ZoneInfo) FindVersion(name string) (VersionInfo, bool) {
	for _, v := range z.Version {
		if v.Name == name {
			return v, true
		}
	}
	return VersionInfo{}, false
}

// RoomBrief 是 QueryZoneRoomListCMsg(1025) 响应中的一条房间摘要。
//
// 实测（2026-08-29）：1025 响应并非此前注释所称的「58105 强加密、Go 端不可解析」——
// 只要请求体为 f1=uid f2=token（无额外参数），响应就是明文 protobuf：
//
//	f1 = ret(1=成功)
//	f3 = RoomInfo（重复，每个房间一条，本次实测 187 条，房间号 1..187）
//	     f1 = roomId
//	     f2 = RoomCfgInfo{ f1=房间名, f5=versionId, f12=obStatus, f23=创建时间戳 }
//	     f4 = { f1=hostUid, f3=1, f4=roomId }
//	     f5 = { f1=uid, f2=slot, f4=roomId }（重复，通常为精简列表）
//	     f6 = { ... }（重复，座位/版本信息）
//	     f7 = hostUid
//	     f12 = { f1=hostUid }
//	f4 = 分页/统计信息
//
// 房间号必须取自该列表：超出列表范围（如 188）建房会返回
// ret=-19 "ROOM_NOT_FOUND 188"，此前「随机 1..150 盲猜」的做法无法保证命中。
type RoomBrief struct {
	ID      uint32 // 房间号
	Name    string // 房间名（空串表示该号当前无人使用）
	Host    uint32 // 房主 uid；0 表示该号空闲（可建房）
	Ob      uint32 // 观战设置（RoomCfgInfo.f12）
	Version uint32 // 游戏版本 ID（RoomCfgInfo.f5）
}

// Free 返回该房间号是否空闲（无人建房），空闲号可直接建房当房主
func (r RoomBrief) Free() bool { return r.Host == 0 }

// QueryRooms 发送 QueryZoneRoomListCMsg(1025) 拉取本区房间列表。
// 返回按房间号升序排列的房间摘要；列表为空时返回错误（通常是 token 未激活或已在房内）。
func (c *Client) QueryRooms(ctx context.Context, addr string, uid uint32, token string) ([]RoomBrief, error) {
	resp, err := c.gate.Zone(ctx, addr, protocol.QueryZoneRoomListCMsg, protocol.FieldsWithAuth(uid, token))
	if err != nil {
		return nil, fmt.Errorf("room: query room list: %w", err)
	}
	f := proto.Parse(resp)
	if ret := int64(proto.GetVarint(f, 1)); ret != 1 {
		return nil, fmt.Errorf("room: query room list ret=%d err=%q", ret, proto.GetString(f, 2))
	}
	var out []RoomBrief
	for _, r := range proto.GetAll(f, 3) {
		rf := proto.Parse(r.B)
		b := RoomBrief{
			ID:   uint32(proto.GetVarint(rf, 1)),
			Host: uint32(proto.GetVarint(rf, 7)),
		}
		if cfg := proto.GetBytes(rf, 2); cfg != nil {
			cf := proto.Parse(cfg)
			b.Name = proto.GetString(cf, 1)
			b.Ob = uint32(proto.GetVarint(cf, 12))
			b.Version = uint32(proto.GetVarint(cf, 5))
		}
		out = append(out, b)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	if len(out) == 0 {
		return nil, errors.New("room: empty room list")
	}
	return out, nil
}

// EnterResult 是建房/进房响应解析结果
type EnterResult struct {
	Ret      uint32 // EnterRoomSMsg.f1
	Slot     uint32 // EnterRoomSMsg.f3=slot（观战=255；建房成功无此字段）
	RoomID   int32  // RoomInfo.f1
	RoomName string // RoomInfo.f2.f1
	HostUid  uint32 // RoomInfo.f7
	IsHost   bool   // RoomInfo.f7 == uid：本机是房主
	IsPlayer bool   // 本机出现在玩家列表 RoomInfo.f5（进他人房间时含本机）
}

// CreateRoom 发送 EnterRoomCMsg(1026) 建房（f3=roomId f4=1 f5=cfg f8=1），解析进入结果。
//
// 成功响应：顶层 f1=1，RoomInfo 在 f4 内，其中 f7=本机 uid（故 IsHost=true），
// 且**不含** f5 玩家列表——所以不能靠 IsPlayer 判断建房成功，必须用 IsHost。
// 房间号取自 QueryRooms（1025 列表）中的空闲号（Host==0）；若该号已被占用会返回
// -87 "WRONG_ROOM_STATUS <id> RS_GAMING"；若号不在列表范围内返回 -19 "ROOM_NOT_FOUND <id>"。
func (c *Client) CreateRoom(ctx context.Context, addr string, uid uint32, token string, roomID int32, cfg []byte) (*EnterResult, error) {
	resp, err := c.gate.Zone(ctx, addr, protocol.EnterRoomCMsg, protocol.BuildCreateRoom(uid, token, roomID, cfg))
	if err != nil {
		return nil, fmt.Errorf("room: create room: %w", err)
	}
	return c.parseEnterResult(resp, uid)
}

// JoinRoom 发送 EnterRoomCMsg(1026) 进入已有房间（不带 isCreate / cfg）：
// 用于“加入他人游戏 / 观战”。房间号存在且可进入时服务器返回 Ret=1，本机以观战身份进入：
// 顶层 f3=255（观战座位号），RoomInfo.f5 玩家列表里能查到本机（IsPlayer=true，slot=255），
// RoomInfo.f7 是房主 uid（≠本机），故 IsHost=false。房间号不存在时返回 Ret≠1。
//
// 房间号调用方必须取自 QueryRooms（1025 房间列表）。
// 更正（2026-08-29）：本函数原先注释称「1024/1025 响应为 58105 强加密、Go 端无解密能力，
// 只能随机选号盲试」——这个判断是错的，实际 1025 请求体只带 f1=uid f2=token 时返回的就是
// 明文 protobuf，可直接解析出全部房间号与占用情况。该错误注释直接导致了长期的盲猜实现。
func (c *Client) JoinRoom(ctx context.Context, addr string, uid uint32, token string, roomID int32) (*EnterResult, error) {
	resp, err := c.gate.Zone(ctx, addr, protocol.EnterRoomCMsg, protocol.BuildEnterRoom(uid, token, roomID))
	if err != nil {
		return nil, fmt.Errorf("room: join room: %w", err)
	}
	return c.parseEnterResult(resp, uid)
}

// parseEnterResult 解析 EnterRoomCMsg(1026) 的进入结果（建房/进房共用）
func (c *Client) parseEnterResult(resp []byte, uid uint32) (res *EnterResult, rerr error) {
	defer func() {
		if r := recover(); r != nil {
			c.log.Error("room: parseEnterResult PANIC", "recovered", r, "respLen", len(resp), "uid", uid)
			rerr = fmt.Errorf("room: parse enter result panic: %v", r)
		}
	}()
	body, err := parseZoneResp(resp)
	if err != nil {
		return nil, err
	}
	f := proto.Parse(body)
	res = &EnterResult{Ret: uint32(proto.GetVarint(f, 1))}
	res.Slot = uint32(proto.GetVarint(f, 3))
	c.log.Info("room: parseEnterResult", "ret", res.Ret, "slot", res.Slot, "bodyLen", len(body),
		"errMsg", proto.GetString(f, 2))
	if r := proto.GetBytes(f, 4); r != nil {
		rf := proto.Parse(r)
		res.RoomID = int32(proto.GetVarint(rf, 1))
		res.HostUid = uint32(proto.GetVarint(rf, 7))
		res.IsHost = res.Ret == 1 && res.HostUid == uid
		if c2 := proto.GetBytes(rf, 2); c2 != nil {
			res.RoomName = proto.GetString(proto.Parse(c2), 1)
		}
		// 玩家列表在 RoomInfo.f5（此前误用 f4，导致建房成功也判定失败）。
		//
		// 实测两种成功响应的差异（2026-08-29）：
		//   建房成功：f1=1, f4={f1=roomId, f2=cfg, f7=本机uid, ...}，无顶层 f3、无 f5
		//   进他人房间：f1=1, f3=255(观战slot), f4={..., f5={f1=本机uid,f2=255}, f7=房主uid}
		// 因此「是否建房成功当房主」必须用 f4.f7 == uid 判定，不能依赖 playerList。
		for _, p := range proto.GetAll(rf, 5) {
			pf := proto.Parse(p.B)
			if uint32(proto.GetVarint(pf, 1)) == uid {
				res.IsPlayer = true
			}
		}
	}
	c.log.Info("room: parseEnterResult done", "ret", res.Ret, "roomID", res.RoomID,
		"hostUid", res.HostUid, "isHost", res.IsHost, "isPlayer", res.IsPlayer)
	return res, nil
}

// enterRoom 依次尝试候选房间号进入房间，返回进入结果与进入方式。
//   - 先在 freeRooms（列表中的空闲号）上建房，成功则本机为房主 → ("create")
//   - 空闲号全被抢占则进入 occupiedRooms 中的他人房间观战 → ("spectate")
//   - 都失败返回 (nil, "")
//
// 历史坑（2026-08-29 定位）：此前用「随机房间号 1..150 盲猜 + 建房失败就加入同一号」，
// 而建房成功的判定错用了 playerList（建房响应 f4 内根本没有 f5 玩家列表字段），
// 导致建房成功也无法识别；同时盲猜的房间号大多不在服务器的 187 个有效号内，
// 建房被拒（-19 ROOM_NOT_FOUND）后加入同一号又被拒，两条路都走不通。
// 现在房间号全部取自 1025 房间列表，且用 RoomInfo.f7 == uid 判定是否当上房主。
func (c *Client) enterRoom(ctx context.Context, addr string, uid uint32, token string,
	cfg []byte, freeRooms, occupiedRooms []RoomBrief) (*EnterResult, string) {

	for _, r := range freeRooms {
		roomID := int32(r.ID)
		c.log.Info("room: try create", "roomID", roomID, "name", r.Name, "ob", r.Ob)
		res, err := c.CreateRoom(ctx, addr, uid, token, roomID, cfg)
		if err != nil {
			c.log.Warn("room: create room err", "roomID", roomID, "err", err)
			continue
		}
		c.log.Info("room: create result", "roomID", roomID, "ret", res.Ret,
			"isHost", res.IsHost, "room", res.RoomID, "name", res.RoomName)
		if res.Ret == 1 && res.IsHost {
			return res, "create"
		}
	}
	for _, r := range occupiedRooms {
		roomID := int32(r.ID)
		c.log.Info("room: try join", "roomID", roomID, "name", r.Name, "host", r.Host, "ob", r.Ob)
		jr, err := c.JoinRoom(ctx, addr, uid, token, roomID)
		if err != nil {
			c.log.Warn("room: join room err", "roomID", roomID, "err", err)
			continue
		}
		c.log.Info("room: join result", "roomID", roomID, "ret", jr.Ret,
			"slot", jr.Slot, "hostUid", jr.HostUid, "name", jr.RoomName)
		if jr.Ret == 1 {
			return jr, "spectate"
		}
	}
	return nil, ""
}

// pickRooms 从房间列表中挑选建房/进房候选：
//   - 优先返回空闲号（Host==0）：在其上建房可直接当房主
//   - 其后是已占用房间：可进房观战再上位
//
// 每类最多取 limit 个，随机打散避免所有账号挤在同一房间。
func pickRooms(rooms []RoomBrief, limit int) (free, occupied []RoomBrief) {
	rnd := rand.Perm(len(rooms))
	for _, i := range rnd {
		r := rooms[i]
		if r.Free() {
			if len(free) < limit {
				free = append(free, r)
			}
		} else if len(occupied) < limit {
			occupied = append(occupied, r)
		}
		if len(free) >= limit && len(occupied) >= limit {
			break
		}
	}
	return free, occupied
}

// 房间消息返回码（uint32 形式的负数，服务器以 int64 varint 下发）
const (
	retOK = 1 // 成功

	// retWrongGameToken 会话 token 未被网关接受。进区前的 512 未发送/会话失效时出现，
	// 此时继续发任何房间消息都没有意义，必须重新进区重建会话。
	retWrongGameToken = 4294967286 // -10
	// retRoomNotFound 房间已不存在（房主解散/踢出）。
	retRoomNotFound = 4294967277 // -19

	// 以下返回码是「正常但暂无数据」，绝不能当作失败，否则会把正常挂机误判为断线：
	//   -142 SNAPSHOT_NO_SRAM：1092 状态轮询在房主/观战身份下恒定返回（服务器无 SRAM 快照）
	//   -34  SNAPSHOT_TOO_OLD：1032 快照轮询首帧返回，下一帧即 ret=1
	//   -29  NOT_PLAYING：     观战身份发送 1028 心跳被拒（观战本来就不需要心跳）
	//   -35  SLOT_OCCUPIED：   1033 上位时座位已被占用，保持观战即可
	//   -24  ALREADY_IN_ROOM： 已在房间内重复进房
	//   -87  WRONG_ROOM_STATUS：建房时房间处于游戏中
)

// fatalRet 判定房间消息返回码是否意味着会话/房间已失效，需要重建。
// 只有「token 失效」与「房间不存在」是致命的；其余一律视为正常。
func fatalRet(ret uint32) bool {
	return ret != retOK && (ret == retWrongGameToken || ret == retRoomNotFound)
}

// callZone 发送 /zone/2/ 消息并返回服务器返回码。
// err 仅表示传输层失败（HTTP/网络）；协议层返回码通过 ret 返回。
func (c *Client) callZone(ctx context.Context, addr string, msgID uint16, body []byte) (uint32, error) {
	resp, err := c.gate.Zone(ctx, addr, msgID, body)
	if err != nil {
		return 0, err
	}
	f := proto.Parse(resp)
	ret := uint32(proto.GetVarint(f, 1))
	if msg := proto.GetString(f, 2); msg != "" {
		c.log.Debug("room: zone resp", "msgid", msgID, "ret", ret, "msg", msg)
	}
	return ret, nil
}

// Leave 发送 LeaveRoomCMsg(1027) 离开房间
func (c *Client) Leave(ctx context.Context, addr string, uid uint32, token string) error {
	_, err := c.callZone(ctx, addr, protocol.LeaveRoomCMsg, protocol.BuildLeaveRoom(uid, token))
	return err
}

// Heartbeat 发送 RoomHeartBeatCMsg(1028) 房间心跳（约每秒 1 次）
// 返回服务器返回码；ret=1 正常，ret=-29 NOT_PLAYING 表示本机不是玩家（不应发心跳）。
func (c *Client) Heartbeat(ctx context.Context, addr string, uid uint32, token string, seq uint32) (uint32, error) {
	return c.callZone(ctx, addr, protocol.RoomHeartBeatCMsg, protocol.BuildRoomHeartBeat(uid, token, seq))
}

// Status 发送 RoomStatusCMsg(1092) 房间状态轮询（约每 1.6 秒 1 次）
// 实测该消息在房主与观战身份下都恒定返回 -142 SNAPSHOT_NO_SRAM（服务器无快照数据），
// 属于正常现象，调用方不应把它当作失败——保活靠 1028 心跳（玩家）或 1032 快照（观战）。
func (c *Client) Status(ctx context.Context, addr string, uid uint32, token string, seq uint32) (uint32, error) {
	return c.callZone(ctx, addr, protocol.RoomStatusCMsg, protocol.BuildRoomStatus(uid, token, seq))
}

// ChangeRoomSlot 发送 ChangeRoomSlotCMsg(1033) 上位（观战→参与游戏）
// slot: 目标座位号（实测上位用 1）；ret=1 才表示上位成功。
// 实测房间已有玩家时会返回 -35 "SLOT_OCCUPIED <uid>"，此时应保持观战身份，
// 不能误判为成功——否则会开始发送 1028 心跳并被 -29 NOT_PLAYING 拒绝。
func (c *Client) ChangeRoomSlot(ctx context.Context, addr string, uid uint32, token string, slot int32) (uint32, error) {
	return c.callZone(ctx, addr, protocol.ChangeRoomSlotCMsg, protocol.BuildChangeRoomSlot(uid, token, slot))
}

// Snapshot 发送 GetRoomSnapShotReplayCMsg(1032) 观战快照轮询
// 观战身份没有 1028 心跳，靠快照轮询保活；seq 从 1 起递增。
// 首帧常返回 -34 SNAPSHOT_TOO_OLD，后续帧返回 ret=1，均属正常。
func (c *Client) Snapshot(ctx context.Context, addr string, uid uint32, token string, seq uint32) (uint32, error) {
	return c.callZone(ctx, addr, protocol.GetRoomSnapShotReplayCMsg, protocol.BuildSnapshot(uid, token, seq))
}

// OnlineReport 发送 RoomOnlineReportCMsg(1042) 在线时长/贡献度上报
// 房主（建房者/上位成功）身份约每秒发送一次，与 1028 心跳同步；
// tick 为递增计数器（真实客户端每帧约 +52~60），用于服务器累计在线时长（战队任务）。
func (c *Client) OnlineReport(ctx context.Context, addr string, uid uint32, token string, tick uint32) (uint32, error) {
	return c.callZone(ctx, addr, protocol.RoomOnlineReportCMsg, protocol.BuildOnlineReport(uid, token, tick))
}

// pickVersion 从分区配置中选择建房版本与 ROM MD5；找不到时使用内置默认（fc_8bit）
func pickVersion(zi *ZoneInfo) (string, string) {
	if zi != nil {
		if v, ok := zi.FindVersion(HangVersionName); ok && v.Rom1MD != "" {
			return v.Name, v.Rom1MD
		}
	}
	return HangVersionName, HangRom1MD5
}

// candidateZones 返回进区尝试顺序：配置值优先，其后为 zoneFallback（去重、跳过 0）。
func candidateZones(cfg uint32) []uint32 {
	out := make([]uint32, 0, 1+len(zoneFallback))
	seen := make(map[uint32]bool, 1+len(zoneFallback))
	add := func(z uint32) {
		if z == 0 || seen[z] {
			return
		}
		seen[z] = true
		out = append(out, z)
	}
	add(cfg)
	for _, z := range zoneFallback {
		add(z)
	}
	return out
}

// enterZoneAny 依次尝试候选区 ID 进区，成功时把实际进入的区 ID 写回 zoneID。
// 全部候选都失败时返回最后一个错误。
func (c *Client) enterZoneAny(ctx context.Context, addr string, uid uint32, token string, zoneID *uint32) (*ZoneInfo, error) {
	var lastErr error
	for i, z := range candidateZones(*zoneID) {
		if i > 0 {
			c.log.Info("room: trying fallback zone", "zoneID", z, "prevErr", lastErr)
		} else {
			c.log.Info("room: Hang loop: entering zone", "zoneID", z)
		}
		zi, err := c.EnterZone(ctx, addr, uid, token, z)
		if err == nil {
			*zoneID = z
			return zi, nil
		}
		lastErr = err
		c.log.Warn("room: enter zone failed", "zoneID", z, "err", err)
	}
	return nil, lastErr
}

// nonOKTracker 对「非 ok 返回码」做日志节流。
//
// 背景（2026-10-07 排查）：1028 心跳 / 1042 在线上报的非 ok 返回码原先只记 Debug，
// 默认不输出，导致「服务端不再累计游戏时长」这类问题在日志里完全不可见——
// chouyoku 10/7 的战队游戏时长卡在 807 秒后再不增长、白挂 18 小时，
// 程序侧日志却显示一切正常（建房 ret=1、无一条失败告警），无从定位。
//
// 但不能直接改成 Warn：这两条消息约每秒发送一次，若返回码持续异常会刷爆日志
// （实测 1092 状态轮询就常态返回 -142，正因如此它是唯一保留 Debug 的）。
// 策略：返回码变化时立即记录，持续相同时每 60 次记一条。
type nonOKTracker struct {
	ret   uint32
	count int
}

func (t *nonOKTracker) log(lg *slog.Logger, msg string, ret uint32, kv ...any) {
	if ret == retOK {
		t.ret, t.count = 0, 0
		return
	}
	if ret != t.ret {
		t.ret, t.count = ret, 0
	}
	t.count++
	if t.count == 1 || t.count%60 == 0 {
		lg.Warn(msg, append([]any{"ret", ret, "times", t.count}, kv...)...)
	}
}

// Hang 持续房间挂机：517 进区 → 1025 拉房间列表 → 1026 建房/进房 → 挂机循环。
//
// 本函数是「每账号独立」的：由 biz.Worker 在账号自己的 goroutine 里调用（core.runAccount
// 为每个账号各起一个 goroutine），房间列表与候选房间号都是每轮循环现查现选的局部变量，
// 多账号之间不共享任何房间状态，可安全并发。撞号时以服务器房间列表为天然互斥表：
// A 建成后 B 再查询会看到该号 Host=A≠0，自动改试其他候选。
//
// 进房策略（enterRoom）：
//   - 先在列表里的空闲号（Host==0）上建房，成功则本机为房主 → 玩家身份挂机
//     （1028 心跳 + 1042 在线上报 + 1092 轮询）
//   - 空闲号全被抢占则进入他人房间观战，停留一段时间后再 1033 上位；
//     上位失败（座位被占）保持观战挂机（1032 快照 + 1092 轮询，无 1028 心跳），
//     观战累计时长同样计入战队任务
//   - 全部候选都被拒绝时等待 HangPickRetry（30 秒）后重新查询列表再试
//
// 挂机期间若房间消息返回致命错误码（-10 token 失效 / -19 房间不存在），
// 视为会话中断：先离房再重新进区建房。ctx 取消时离开房间并返回。
func (c *Client) Hang(ctx context.Context, addr string, uid uint32, token string, zoneID uint32) error {
	var hbSeq, stSeq, snapSeq uint32
	for {
		// 1. 进入分区，拉取区配置（fc_8bit 版本信息）
		// 配置区 ID 优先；若该区不存在（ZONE_NOT_FOUND）则依次回退，
		// 避免像 2026-08-28 那样整天卡在一个无效区 ID 上。
		zi, err := c.enterZoneAny(ctx, addr, uid, token, &zoneID)
		if err != nil {
			c.log.Warn("room: enter zone failed, retry later", "err", err, "after", HangRetryAfter)
			if !waitCtx(ctx, HangRetryAfter) {
				return ctx.Err()
			}
			continue
		}
		c.log.Info("room: zone entered", "zoneID", zoneID, "zoneName", zi.Name, "versions", len(zi.Version))
		verName, romMD5 := pickVersion(zi)
		c.log.Info("room: picked version", "verName", verName, "romMD5", romMD5)
		cfg := protocol.BuildRoomCfg(HangRoomName, verName, romMD5)

		// 2. 拉取本区房间列表（1025），房间号必须取自列表：
		//    列表外的号码建房会被拒（ret=-19 "ROOM_NOT_FOUND <id>"），
		//    列表内已占用的号码建房也会被拒（ret=-87 "WRONG_ROOM_STATUS <id> RS_GAMING"）。
		rooms, rerr := c.QueryRooms(ctx, addr, uid, token)
		if rerr != nil {
			c.log.Warn("room: query room list failed", "err", rerr, "after", HangRetryAfter)
			if !waitCtx(ctx, HangRetryAfter) {
				return ctx.Err()
			}
			continue
		}
		c.log.Info("room: room list", "total", len(rooms))
		freeRooms, occupiedRooms := pickRooms(rooms, hangCreateTries)

		// 3. 进房策略（enterRoom）：
		//    a) 优先在列表中的空闲号上建房 → 成功则本机是房主（EnterResult.IsHost）
		//    b) 空闲号全被抢占 → 进入他人房间观战，停留后再 1033 上位
		entered, joinedMode := c.enterRoom(ctx, addr, uid, token, cfg, freeRooms, occupiedRooms)
		if entered == nil {
			c.log.Warn("room: all candidate rooms rejected, retry", "after", HangPickRetry)
			if !waitCtx(ctx, HangPickRetry) {
				return ctx.Err()
			}
			continue
		}

		// 4. 观战者尝试上位：先观战停留模拟正常玩家，再发送 1033 上位。
		//    上位失败（座位被占/被拒）时保持观战挂机——观战累计时长同样可领战队任务奖励
		if joinedMode == "spectate" {
			c.log.Info("room: entered occupied room as spectator, wait before take seat",
				"roomID", entered.RoomID, "name", entered.RoomName, "obSlot", entered.Slot)
			if !waitCtx(ctx, HangSpectateBefore) {
				return ctx.Err()
			}
			// 必须校验返回码：房间已有玩家时服务器返回 -35 "SLOT_OCCUPIED <uid>"，
			// 但 HTTP 仍是 200，只看 error 会误判上位成功，进而开始发 1028 心跳被
			// -29 NOT_PLAYING 连续拒绝，最终误判断线重建房间。
			ret, err := c.ChangeRoomSlot(ctx, addr, uid, token, HangSeatSlot)
			if err == nil && ret == retOK {
				joinedMode = "seat"
				c.log.Info("room: took seat, hanging", "roomID", entered.RoomID, "name", entered.RoomName, "seat", HangSeatSlot)
			} else {
				c.log.Warn("room: take seat failed, keep spectating",
					"roomID", entered.RoomID, "ret", ret, "err", err)
			}
		} else {
			c.log.Info("room: created room, hanging", "roomID", entered.RoomID, "name", entered.RoomName)
		}

		// 5. 挂机循环：玩家身份（建房/上位成功）发 1028 心跳 + 1042 在线上报 + 1092 轮询；
		//    观战身份（上位失败保持观战）无 1028，改发 1032 快照轮询 + 1092 轮询保活。
		//    房主退出导致观战者被踢/房间关闭时，轮询连续失败 → 重建房间
		spectating := joinedMode == "spectate"
		// onlineTick 为 1042 上报的递增计数器；初值取较大常数以匹配真实客户端量级，
		// 服务器仅校验递增与频率。每发送一次约 +56（真实客户端实测 52~60 均值）。
		onlineTick := uint32(1124000)
		// 非 ok 返回码的节流记录器（详见 nonOKTracker 注释）
		var hbNonOK, onlineNonOK nonOKTracker
		hbT := time.NewTicker(time.Second)
		stT := time.NewTicker(1600 * time.Millisecond)
		snapT := time.NewTicker(1600 * time.Millisecond)
		hbFail, stFail, snapFail := 0, 0, 0
		broken := false
	loop:
		for {
			select {
			case <-ctx.Done():
				hbT.Stop()
				stT.Stop()
				snapT.Stop()
				leaveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
				if err := c.Leave(leaveCtx, addr, uid, token); err != nil {
					c.log.Warn("room: leave failed", "err", err)
				}
				cancel()
				return ctx.Err()
			case <-hbT.C:
				if spectating {
					continue // 观战身份不发 1028 心跳
				}
				hbSeq++
				ret, err := c.Heartbeat(ctx, addr, uid, token, hbSeq)
				if err != nil {
					hbFail++
					c.log.Warn("room: heartbeat failed", "seq", hbSeq, "err", err)
					if hbFail >= 3 {
						broken = true
						break loop
					}
				} else if fatalRet(ret) {
					c.log.Warn("room: heartbeat fatal ret, session invalid", "seq", hbSeq, "ret", ret)
					broken = true
					break loop
				} else {
					hbNonOK.log(c.log, "room: heartbeat non-ok ret", ret, "seq", hbSeq)
					hbFail = 0
					// 与心跳同步发送 1042 在线时长上报（房主身份保活/战队任务时长累计）
					onlineTick += 56
					if ret, err := c.OnlineReport(ctx, addr, uid, token, onlineTick); err != nil {
						c.log.Warn("room: online report failed", "tick", onlineTick, "err", err)
					} else {
						onlineNonOK.log(c.log, "room: online report non-ok ret", ret, "tick", onlineTick)
					}
				}
			case <-snapT.C:
				if !spectating {
					continue // 玩家身份不发 1032 快照轮询
				}
				snapSeq++
				// 首帧常见 -34 SNAPSHOT_TOO_OLD（服务器快照尚未生成），下一帧即 ret=1，
				// 属正常；只有传输层失败或致命返回码才累计失败。
				ret, err := c.Snapshot(ctx, addr, uid, token, snapSeq)
				if err != nil {
					snapFail++
					c.log.Warn("room: snapshot poll failed", "seq", snapSeq, "err", err)
					if snapFail >= 3 {
						broken = true
						break loop
					}
				} else if fatalRet(ret) {
					c.log.Warn("room: snapshot fatal ret, session invalid", "seq", snapSeq, "ret", ret)
					broken = true
					break loop
				} else {
					snapFail = 0
				}
			case <-stT.C:
				stSeq++
				// 1092 在房主与观战身份下都恒定返回 -142 SNAPSHOT_NO_SRAM，
				// 这是「服务器暂无快照」而非失败：把它当失败会让每次挂机都在
				// 5 秒内被误判断线并重建房间，表现为「建房成功却立刻重开」。
				ret, err := c.Status(ctx, addr, uid, token, stSeq)
				if err != nil {
					stFail++
					c.log.Warn("room: status poll failed", "seq", stSeq, "err", err)
					if stFail >= 3 {
						broken = true
						break loop
					}
				} else if fatalRet(ret) {
					c.log.Warn("room: status fatal ret, session invalid", "seq", stSeq, "ret", ret)
					broken = true
					break loop
				} else {
					stFail = 0
				}
			}
		}
		hbT.Stop()
		stT.Stop()
		snapT.Stop()
		if broken {
			c.log.Warn("room: hang session broken, will recreate room")
			// 必须先离房再重建：否则房间号仍被本机占用，下一轮既建不了
			// （-87 WRONG_ROOM_STATUS），进同一间又被拒（-24 ALREADY_IN_ROOM），
			// 会卡在「每 30 秒重试但永远进不去」的死循环里。
			leaveCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			if err := c.Leave(leaveCtx, addr, uid, token); err != nil {
				c.log.Warn("room: leave before recreate failed", "err", err)
			}
			cancel()
			if !waitCtx(ctx, 5*time.Second) {
				return ctx.Err()
			}
		}
	}
}

func waitCtx(ctx context.Context, d time.Duration) bool {
	t := time.NewTimer(d)
	defer t.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-t.C:
		return true
	}
}
