// Package session 实现登录状态机：HTTP 登录取 uid/token/gate，供业务层使用
package session

import (
	"context"
	"crypto/md5"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"time"

	"youjuhang/internal/config"
	"youjuhang/internal/gate"
	"youjuhang/internal/proto"
	"youjuhang/internal/protocol"
)

// Session 是单个账号的登录会话
type Session struct {
	Account  *config.Account
	Gate     *gate.Client
	UID      uint32
	Token    string
	GateAddr string // "ip:port"
	LoginAt  time.Time
}

// enterLobbyTries EnterLobby(512) 的最大尝试次数
const enterLobbyTries = 3

// enterLobbyTimeout 单次 EnterLobby 请求的超时上限。
//
// 必须远小于网关 HTTP 客户端的默认 30s：「登录成功」是在 Login 返回**之后**
// 才由 core 打印的，而 Login 会同步等待本函数。网关不响应时，
// 旧实现的 5 次 × 30s ≈ 165s 会让账号在 UI 上"登录中"假死近 3 分钟，
// 且日志里什么都没有，极易被误判为「登录卡死/账号有问题」。
// 512 失败并不阻断登录（/game/ 侧任务/战队/签到不依赖它），
// 快速失败后由房间挂机按既有重试策略兜底，代价远小于假死。
const enterLobbyTimeout = 8 * time.Second

// enterLobby 发送 EnterLobbyCMsg(512) 激活登录 token，失败时重试几次。
//
// 这一步是房间挂机能否工作的关键前提，实测（2026-08-29）结论：
//   - 不发送 512：/game/ 消息（516 查询区 / 517 进区 / 任务 / 战队）照常成功，
//     但 /zone/2/ 的房间消息（1025 房间列表 / 1026 进房建房 / 1027 离房 /
//     1028 心跳 / 1032 快照 / 1042 上报）恒定返回 ret=-10 "WRONG_GAME_TOKEN"。
//   - 发送 512 后：同样的 1026 请求返回 ret=1，建房/进房/观战/上位全部正常。
//
// 历史坑：此前程序登录后直接跳过 512 进区，表现为「进区成功但建房永远返回 -10」，
// 因 517 成功造成「token 没问题」的错觉，误判为房间号/建房参数问题，长期未定位。
func enterLobby(ctx context.Context, gc *gate.Client, s *Session) error {
	var lastErr error
	for i := 0; i < enterLobbyTries; i++ {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		// 单次请求独立超时：网关无响应时快速失败，不拖慢整个登录流程
		attemptCtx, attemptCancel := context.WithTimeout(ctx, enterLobbyTimeout)
		resp, err := gc.Game(attemptCtx, s.GateAddr, protocol.EnterLobbyCMsg,
			protocol.BuildEnterLobby(s.UID, s.Token))
		attemptCancel()
		if err == nil {
			if ret := int64(proto.GetVarint(proto.Parse(resp), 1)); ret != 1 {
				lastErr = fmt.Errorf("enter lobby ret=%d err=%q", ret, proto.GetString(proto.Parse(resp), 2))
			} else {
				return nil
			}
		} else {
			lastErr = err
		}
		select {
		case <-time.After(time.Duration(i+1) * time.Second):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	return lastErr
}

// Login 完成登录：HTTP/2 → uid/token/gate，带指数退避重试
func Login(ctx context.Context, gc *gate.Client, acc *config.Account) (*Session, error) {
	hash := pwdHash(acc.Password)
	backoff := time.Second
	for {
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// raknetId：随机 uint64（高位置 1，与客户端一致）
		raknet := uint64(rand.Uint32())<<32 | uint64(rand.Uint32()) | 0x1000000000000000
		res, err := gc.Login(ctx, acc.Name, hash, acc.MAC, raknet)
		if err == nil {
			sess := &Session{
				Account:  acc,
				Gate:     gc,
				UID:      res.UID,
				Token:    res.Token,
				GateAddr: res.Gate,
				LoginAt:  time.Now(),
			}
			// 必须先进大厅激活 token，否则 /zone/2/ 房间消息全部 -10 WRONG_GAME_TOKEN
			if lerr := enterLobby(ctx, gc, sess); lerr != nil {
				// 失败不阻断登录：/game/ 侧业务（任务/战队/签到）不依赖 512，
				// 仅房间挂机会失败并在 Hang 内按 1 小时重试。
				slog.Warn("进大厅(512)失败，房间挂机将不可用", "account", acc.Name, "err", lerr)
			}
			return sess, nil
		}
		// 服务器明确拒绝（密码错误/账号不存在/封禁等）：**立即上抛，不重试**。
		//
		// 这里过去是无差别的无限重试（1s→2s→…→5min 永不停止），导致密码错的账号
		// 被反复提交登录——既浪费资源，又有被风控判定为撞库的风险，且上层
		// （core.runAccount）永远收不到错误、无法触发自动禁用。
		// 凭据类失败重试一万次结果都一样，交给上层按次数决定何时禁用账号。
		if errors.Is(err, gate.ErrLoginRejected) {
			return nil, err
		}
		if ctx.Err() != nil {
			return nil, ctx.Err()
		}
		// 仅网络/传输类失败（可恢复）走退避重试
		wait := backoff + time.Duration(rand.Float64()*0.2*float64(backoff))
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
		if backoff < 5*time.Minute {
			backoff *= 2
		}
	}
}

// pwdHash 计算密码 MD5 大写（实测 field3 = MD5(密码).upper()）
func pwdHash(pwd string) string {
	h := md5.Sum([]byte(pwd))
	return hex.EncodeToString(h[:])
}

// PwdHash 导出密码哈希（供日志脱敏对比用）
func PwdHash(pwd string) string {
	return pwdHash(pwd)
}

// Release 主动登出（LeaveZoneCMsg 534）：通知游聚清空本 uid 的登录会话。
//
// 为什么需要它（2026-09-01 事故）：
// exe 退出（Ctrl+C / 托盘退出 / SIGTERM / 单账号停止）时，仅 goroutine 结束、连接被 GC，
// 游聚侧 wing 的登录会话并不会被清除（HTTP/2 token 化、gate 连接共享，无"注销"副作用）。
// 于是该 uid 在服务器侧残留"僵尸 session"——isOnline 恒为 1——把被本账号顶掉的对端
// （例如服务器上的挂机 wing）永远卡在"等待恢复"。
// 发送 534 让游聚显式清会话后，对端才能 isOnline=0 安全重连恢复。
//
// 必须在 ctx 取消后也能发出：调用方多在 ctx.Err()!=nil 的分支调用，故这里用独立
// 的带超时 context，不受主 ctx 取消影响。
func (s *Session) Release() {
	if s.Gate == nil || s.GateAddr == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	if _, err := s.Gate.Game(ctx, s.GateAddr, protocol.LeaveZoneCMsg, protocol.FieldsWithAuth(s.UID, s.Token)); err != nil {
		slog.Warn("session release (LeaveZone 534) failed", "uid", s.UID, "gate", s.GateAddr, "err", err)
	}
}
