// 好友/社交协议：PlayerSocialCMsg(1176) 只读查询指定 uid 的在线状态
// 实测（2026-08-26）：action=1 + uidList=[目标uid] 返回 playerQuickList，
// 其中 SocialPlayerQuickInfo.f100 = isOnline。该查询为只读，不会顶掉任何会话。
// 注意（2026-09-01 真机复测）：action=1 对**好友与非好友的任意存在 uid**都返回 isOnline，
// 恢复挂机无需依赖好友关系（详见 QueryUserOnline 与 tools/probe_social.py）。
package biz

import (
	"context"
	"errors"
	"fmt"

	"youjuhang/internal/proto"
	"youjuhang/internal/session"
)

// CMsgPlayerSocial 好友/社交数据消息号（proto: PlayerSocialCMsg/PlayerSocialSMsg）
const CMsgPlayerSocial = 1176

// PlayerSocialActionGetData 获取社交数据（好友/关注列表与指定 uid 在线状态）
const PlayerSocialActionGetData = 1

// UserOnlineResult 是单个 uid 的在线状态查询结果
type UserOnlineResult struct {
	UID      uint32
	Nickname string
	Online   bool
}

// QueryUserOnline 用侦查账号的会话查询目标 uid 是否在线。
// 只读查询，不会影响任何会话。
//
// 实测确认（2026-09-01，tools/probe_social.py 真机验证）：协议 1176 action=1
// 按 uidList 查询，对**任意存在的 uid（好友或非好友均可）**返回其
// SocialPlayerQuickInfo，其中 f100 = isOnline。因此恢复挂机无需依赖好友关系，
// 非好友账号也能可靠查到在线/离线状态。
// 区分：
//   - ret==1 且列表含目标 → Online = f100（权威，好友/非好友都准）
//   - ret!=1（服务器拒绝/会话失效）或列表不含目标（异常） → 返回 err，
//     交由调用方切换侦查账号或走兜底，绝不臆测为"离线"以免抢占正在游戏的用户。
func QueryUserOnline(ctx context.Context, s *session.Session, targetUID uint32) (*UserOnlineResult, error) {
	body := proto.FieldVarint(nil, 1, uint64(s.UID))
	body = proto.FieldBytes(body, 2, []byte(s.Token))
	body = proto.FieldVarint(body, 3, PlayerSocialActionGetData)
	body = proto.FieldVarint(body, 4, uint64(targetUID))
	resp, err := s.Gate.Game(ctx, s.GateAddr, CMsgPlayerSocial, body)
	if err != nil {
		return nil, fmt.Errorf("query online(%d) http: %w", targetUID, err)
	}
	fields := proto.Parse(resp)
	if ret := proto.GetVarint(fields, 1); ret != 1 {
		errStr := proto.GetString(fields, 2)
		return nil, fmt.Errorf("query online(%d) rejected ret=%d err=%s", targetUID, ret, errStr)
	}
	// f4 = playerQuickList (repeated SocialPlayerQuickInfo)
	for _, f := range proto.GetAll(fields, 4) {
		qf := proto.Parse(f.B)
		uid := uint32(proto.GetVarint(qf, 1))
		if uid != targetUID {
			continue
		}
		return &UserOnlineResult{
			UID:      uid,
			Nickname: proto.GetString(qf, 2),
			Online:   proto.GetVarint(qf, 100) == 1,
		}, nil
	}
	// 目标 uid 不在返回列表：对存在的账号不应发生（离线也应带 isOnline=0 返回），
	// 视为查询异常，交由调用方兜底，避免误判离线抢占用户。
	return nil, fmt.Errorf("query online(%d): target not in playerQuickList", targetUID)
}

// ErrProbeFailed 侦查失败（侦查账号会话失效等），调用方可回退到普通等待逻辑
var ErrProbeFailed = errors.New("probe session failed")
