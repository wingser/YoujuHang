// 战队贡献捐献：每日捐献贡献度（协议 596/f3=15），余额低于下限跳过。
// 幂等：按账号持久化当日已捐献量，避免重启/重复登录导致重复捐献。
// 协议实测见 docs/protocol_notes.md「战队贡献捐献」。
//
// 2026-09-01：ContributeStore 类型已合并到 StateStore（见 state_store.go），
// 本文件只剩"整笔捐献 + 整百规整"的业务逻辑。
package biz

import (
	"context"
	"fmt"
	"time"

	"youjuhang/internal/proto"
	"youjuhang/internal/protocol"
)

// contributeAmountStep 服务器接受的捐献量粒度（必须是它的整数倍）。
// 实测：非整百的捐献量会被整单拒绝，返回 "count must be a multiple of 100"。
const contributeAmountStep = 100

// roundContributeAmount 把捐献量向下取整到 contributeAmountStep 的倍数。
//
// 为什么必须规整：amount 会被"余额 - 下限"裁剪（不能把余额捐到下限以下），
// 裁剪结果通常是非整百的值（如余额 6250、下限 3000 → 3250）。
// 直接提交会被服务器拒绝，而 need 没有减少，于是每个主循环（每分钟）重试一次，
// 实测一夜产生 564 条告警且毫无进展。向下取整后既能捐、又不会超过可捐上限；
// 不足一个步长时返回 0，调用方据此退出，避免无效重试。
func roundContributeAmount(amount int) int {
	if amount <= 0 {
		return 0
	}
	return amount / contributeAmountStep * contributeAmountStep
}

// ContributeResult 是单次捐献结果
type ContributeResult struct {
	Ret           uint64
	Success       bool
	BalanceBefore uint64 // 捐前贡献度余额
	BalanceAfter  uint64 // 捐后贡献度余额
	Msg           string
}

// maybeContribute 战队贡献捐献主逻辑：
//   - 仅已加入战队的账号开启捐献（未加入战队默认不开启）
//   - 每日目标贡献度（contribute_daily，默认 3000）
//   - 整笔一次性捐献（按用户原意：余额超过 3000 → 捐 3000；不足则不执行）
//   - 按账号记录当日已捐，重复登录/重启不重复捐献
//
// 2026-09-01 修正：原实现按 step=1000 分多次，且 amount 受 maxDon=余额-下限 限制。
// 这导致余额 4300 时只捐 1300（1000+300）就被 maxDon 卡死，done 永远凑不到 3000。
// 新逻辑：**单笔 amount = need（向下取整 100 的倍数）**，无 maxDon 限制；
// 一次性完成今日目标，无循环，无 sleep，不再有"半夜 564 条告警"那种"卡在卡边界"的问题。
func (w *Worker) maybeContribute(ctx context.Context) error {
	if !w.cfg.ContributeEnabled {
		return nil
	}
	if !w.hasGang(ctx) {
		w.log.Debug("账号未加入战队，跳过贡献捐献")
		return nil
	}
	goal := w.cfg.ContributeDaily

	done := w.stateStore.ContributeToday(w.sess.UID)
	need := goal - done
	if need <= 0 {
		return nil // 今日已完成
	}

	bal, err := w.queryContributeBalance(ctx)
	if err != nil {
		return classify(err)
	}
	// 用户原始需求（2026-09-01）：余额超过 goal 才执行，否则不执行。
	// 这里用捐款**前**的余额判断，与"余额低于下限不执行"语义一致：
	//   余额 <= goal → 不执行（捐不起 3000 整笔）
	//   余额 >  goal → 整笔 amount = need（向下取整 100 的倍数）
	if uint64(goal) >= bal {
		w.log.Info("余额不足本次捐献目标，跳过", "balance", bal, "goal", goal, "today", done)
		return nil
	}

	amount := roundContributeAmount(int(need))
	if amount <= 0 {
		return nil // need < 100（之前捐过的尾数），无意义
	}

	w.log.Info("执行战队贡献捐献（整笔）",
		"amount", amount, "balance", bal, "today", done, "goal", goal)
	res, err := w.contributeOnce(ctx, amount)
	if err != nil {
		w.log.Warn("贡献捐献失败", "err", err)
		return nil
	}
	if !res.Success {
		w.log.Warn("贡献捐献被拒", "ret", res.Ret, "msg", res.Msg)
		return nil
	}
	done += amount
	w.statsMu.Lock()
	w.contributeToday = done
	w.statsMu.Unlock()
	_ = w.stateStore.ContributeAdd(w.sess.UID, amount)
	w.log.Info("贡献捐献成功",
		"amount", amount, "balance_after", res.BalanceAfter, "today", done, "goal", goal)
	return nil
}

// queryTeamInfo 查询战队详情（596/f3=3），返回是否已加入战队及贡献度余额。
// 返回的 hasGang 用于判定战队任务（战盟挂机/战队捐献）是否开启。
//
// 响应结构（2026-08-31 抓包 + 探针实测，三账号对照）：
//   顶层：f1=ret(1=成功) f6=战队数据块
//   f6 子字段（**有战队** chouyoku/wingser）：
//     f1=战队ID(10104) f2=战队公告 f3=成员列表 f4=自身UID f5=贡献度余额(6250/3250)
//   f6 子字段（**无战队** youjugua，客户端只显示"战队列表"）：
//     f3=可加入战队列表 f4=自身UID   ← 无 f1(战队ID)、无 f5(余额)
//
// 严重更正（2026-08-31）：此前用 `f6.f4 != 0` 判定"已加入战队"是**错误的**。
// f6.f4 恒为**当前账号自身的 UID**（实测 chouyoku=6017780 / wingser=5571561 /
// youjugua=9068537，均与登录返回的 uid 一致），因此该判据恒为真，
// 导致所有账号（含从未加入战队的新注册账号）都被误判为"已加入战队"。
// 正确判据是 f6.f1（战队 ID）：有战队时为该战队的 ID，无战队时字段不存在(0)。
func (w *Worker) queryTeamInfo(ctx context.Context) (hasGang bool, balance uint64, err error) {
	body := protocol.BuildTeamInfo(w.sess.UID, w.sess.Token)
	resp, err := w.sess.Gate.Game(ctx, w.sess.GateAddr, protocol.TeamContributeCMsg, body)
	if err != nil {
		return false, 0, classify(err)
	}
	fields := proto.Parse(resp)
	ret := proto.GetVarint(fields, 1)
	if ret != 1 {
		w.log.Debug("战队详情查询非成功，视为未加入战队", "ret", ret, "msg", proto.GetString(fields, 2))
		return false, 0, nil
	}
	f6 := proto.GetBytes(fields, 6)
	if f6 == nil {
		return false, 0, fmt.Errorf("战队详情缺少成员字段")
	}
	sub := proto.Parse(f6)
	gangID := proto.GetVarint(sub, 1) // f6.f1 = 战队 ID（无战队时不存在）
	if gangID == 0 {
		// 无 f1(战队ID) → 未加入任何战队，f6 只是可加入战队的列表
		w.log.Debug("战队详情无战队ID字段，判定为未加入战队",
			"uid", w.sess.UID, "f6_len", len(f6))
		return false, 0, nil
	}
	w.log.Debug("判定为已加入战队", "gang_id", gangID,
		"balance", proto.GetVarint(sub, 5))
	return true, proto.GetVarint(sub, 5), nil
}

// hasGang 判定当前账号是否已加入战队（当天结果缓存，跨天自动重新检测）。
// 仅已加入战队的账号开启战队任务（战盟挂机/战队捐献），未加入战队默认不开启。
func (w *Worker) hasGang(ctx context.Context) bool {
	now := time.Now()
	day := dayKey(now)
	w.gangMu.Lock()
	if w.gangCheckedDay == day {
		ok := w.gangOK
		w.gangMu.Unlock()
		return ok
	}
	w.gangMu.Unlock()

	ok, _, err := w.queryTeamInfo(ctx)
	if err != nil {
		w.log.Warn("战队状态检测失败，按未加入战队处理", "err", err)
		return false
	}
	w.gangMu.Lock()
	w.gangOK = ok
	w.gangCheckedDay = day
	w.gangMu.Unlock()
	if ok {
		w.log.Info("检测到账号已加入战队，开启战队任务（挂机/捐献）")
	} else {
		w.log.Info("账号未加入战队，战队任务默认不开启（挂机/捐献）")
	}
	return ok
}

// queryContributeBalance 查询贡献度余额 root.f6.f5（调用方需先确认已加入战队）
func (w *Worker) queryContributeBalance(ctx context.Context) (uint64, error) {
	hasGang, bal, err := w.queryTeamInfo(ctx)
	if err != nil {
		return 0, err
	}
	if !hasGang {
		return 0, fmt.Errorf("账号未加入战队")
	}
	return bal, nil
}

// contributeOnce 单次贡献捐献（596/f3=15, f9={f1=amount}）
func (w *Worker) contributeOnce(ctx context.Context, amount int) (*ContributeResult, error) {
	body := protocol.BuildTeamContribute(w.sess.UID, w.sess.Token, uint32(amount))
	resp, err := w.sess.Gate.Game(ctx, w.sess.GateAddr, protocol.TeamContributeCMsg, body)
	if err != nil {
		return nil, classify(err)
	}
	fields := proto.Parse(resp)
	res := &ContributeResult{Ret: proto.GetVarint(fields, 1)}
	if res.Ret != 1 {
		res.Msg = proto.GetString(fields, 2)
		return res, nil
	}
	res.Success = true
	if f8 := proto.GetBytes(fields, 8); f8 != nil {
		sub := proto.Parse(f8)
		res.BalanceBefore = proto.GetVarint(sub, 1)
		res.BalanceAfter = proto.GetVarint(sub, 2)
	}
	return res, nil
}
