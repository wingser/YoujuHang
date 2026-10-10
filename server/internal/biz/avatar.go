package biz

import (
	"context"
	"strconv"
	"time"

	"youjuhang/internal/mall"
)

// 瞬态失败（商城超时、页面异常响应）后的重试退避：首次 10 分钟（≈主循环一轮），
// 之后翻倍递增，上限 2 小时。
// 目的：既能自愈，又不会在商城长时间不可用时压测式重试（最多约 12 次/天）。
const (
	avatarRetryMinBackoff = 10 * time.Minute
	avatarRetryMaxBackoff = 2 * time.Hour
)

// maybeDressAvatar 保证账号佩戴着「经验加成最高且未过期」的头像。
//
// 背景（2026-09-09 抓包确认，协议详见 docs/avatar_api.md）：
// 个人空间的部分头像带平台经验加成（additional_exp，实测 1.1/1.2/1.4 等档位），
// 但都是限时的（页面显示「剩余N天」，30 天一续），到期加成即失效。
//
// 【2026-09-09 按用户需求重写决策逻辑】此前"拉列表算最优 + 与内存记录比较，
// 不同就佩戴"，导致程序每次重启都会重新佩戴一次（内存归零），且查"当前佩戴"
// 的方式根本是错的。正确做法（用户指出）：个人空间初始化页
// GET /?token=..&userid=..&space=1 的响应里就有当前佩戴头像（userAvatar），
// **以服务端查询结果为唯一事实**：
//
//	当前佩戴的加成头像仍有效（Exp>1 且未过期）→ 什么都不做，绝不重复佩戴；
//	未佩戴 / 佩戴无加成 / 已过期             → 才从列表中选最优执行佩戴。
//
// 因此重启、重登都不会触发佩戴动作；只有真正需要时才佩戴。
//
// 频率：主循环每轮调用（首轮 + 每轮循环），按 avatarKey（日期）去重 → 每天一次；
// 常态下每天只有一个 GET 请求（space 页），只有需要更换时才翻页拉全量列表。
// 瞬态失败（商城超时 / 页面异常）会回滚当日标记并按退避重试，见 avatarTransient。
// startAvatarCheck 异步执行一次头像检查（不阻塞业务主循环）。
//
// 为什么异步（2026-09-10）：头像检查要访问商城（space 页 + 可能的翻页拉全量列表），
// 实测需要 3~4 秒（服务器日志 wingser 15:27:09 → 15:27:12，count=49）。
// 原先同步放在首轮业务里，会连带推迟战队捐献、**房间挂机启动**，
// 以及最关键的 notifyStats（状态推送到 UI），表现为「登录后账号数据加载很慢」。
//
// 改为异步后：
//   - 主循环立即继续：捐献、挂机启动、状态推送都不再等头像；
//   - 头像结果出来后单独再推一次状态，UI 的头像行随后填充（其余数据早已显示）；
//   - 头像未出结果前，前端不显示该行（avatar 为 nil 时 avatarHtml 返回空）。
func (w *Worker) startAvatarCheck(ctx context.Context) {
	if !w.avatarDue(time.Now()) {
		return
	}

	go func() {
		// 子 goroutine 必须挂 recover：这里的 panic 不会被 main 的 defer 捕获，
		// 会直接终结整个进程（2026-08-31 事故教训）。
		defer recoverGoroutine(w.log, "经验头像检查")
		// 无论成功、失败还是 panic，都要清标志，否则头像检查会被永久卡死。
		defer func() {
			w.statsMu.Lock()
			w.avatarChecking = false
			w.statsMu.Unlock()
		}()
		w.maybeDressAvatar(ctx)
		// 头像状态变化（佩戴成功 / 失败 / 无可用）后推送一次，UI 随即展示
		w.notifyStats()
	}()
}

// avatarDue 报告现在是否应发起一次头像检查；是则原子地把 avatarChecking 置位，
// 调用方必须在检查结束时清零。
//
// 抽出本函数有两个目的：
//  1. 把全部启动条件集中一处，并在同一把 statsMu 下判断+置位，保证原子性；
//  2. 让这些条件可被单测直接覆盖——startAvatarCheck 一旦放行就要起 goroutine
//     访问商城，测试里不能走那条路。
//
// 四个条件（任一命中即不起）：
//  1. 功能未启用（cfg.AvatarEnabled=false）；
//  2. 当天已处理过（avatarKey == 今天）——保证常态下每天只有一次商城请求；
//  3. 已有检查在跑（avatarChecking）——避免并发佩戴导致状态抖动（2026-09-10）；
//  4. 处于瞬态失败退避窗口内（now < avatarRetryAt）——避免商城故障时每轮都重试。
//     未设置退避时 avatarRetryAt 为零值，Before 恒为 false，等价于"不退避"。
func (w *Worker) avatarDue(now time.Time) bool {
	if !w.cfg.AvatarEnabled {
		return false
	}
	w.statsMu.Lock()
	defer w.statsMu.Unlock()
	if w.avatarChecking || w.avatarKey == dayKey(now) || now.Before(w.avatarRetryAt) {
		return false
	}
	w.avatarChecking = true
	return true
}

// maybeDressAvatar 执行一次头像检查（同步，供 startAvatarCheck 在 goroutine 中调用）。
// 函数内部已按日期去重，并有 30 秒总超时，返回错误仅用于记录。
func (w *Worker) maybeDressAvatar(ctx context.Context) error {
	if !w.cfg.AvatarEnabled {
		return nil
	}
	now := time.Now()
	key := dayKey(now)

	w.statsMu.Lock()
	if key == w.avatarKey {
		w.statsMu.Unlock()
		return nil // 当日已处理
	}
	w.avatarKey = key
	w.statsMu.Unlock()

	// 独立总超时：商城是明文 HTTP 且可能串行多个请求（space 页 + 翻页列表 + 佩戴），
	// 差网络下单个请求就可能吃满 30s 客户端超时。本函数在业务主循环里同步执行，
	// 必须用总超时把最坏阻塞压住，避免堵死签到/任务/捐献（"服务器卡死"教训）。
	dressCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	// 第一步：拉个人空间「我的装扮」页——一个请求同时得到当前佩戴与拥有列表
	sp, err := w.mall.GetSpace(dressCtx, w.sess.UID, w.sess.Token)
	if err != nil {
		w.log.Warn("个人空间获取失败", "err", err)
		w.avatarTransient("个人空间获取失败")
		return nil
	}
	// 状态一致性保护（2026-09-10）：拿不到「当前佩戴」就绝不执行佩戴决策。
	//
	// 页面异常时（限流页 / 未登录跳转 / 解析不到 userAvatar）WornURL 为空，
	// 此时若照常走「选最优佩戴」，等于在完全不知道用户当前戴了什么的情况下
	// 覆盖它——很可能把用户手动选的头像换掉。宁可本次不处理（每天都有机会），
	// 也不能在信息不全时误覆盖。
	if sp.WornURL == "" {
		w.log.Warn("个人空间未返回当前佩戴头像（页面异常？），放弃本次处理以避免误覆盖",
			"count", len(sp.Items))
		w.avatarTransient("个人空间页面异常")
		return nil
	}
	w.log.Info("个人空间获取完成", "count", len(sp.Items), "worn_url", sp.WornURL)

	// 提前更换阈值（2026-10-03 需求，方案 A）：剩余 ≤ renewBefore 天时主动换新，
	// 避免头像到期后被服务端摘下、要等下次检查才换上（实测最长空档约 1 天）。
	// 默认 1；配置为 0 则恢复"只在彻底过期后才换"的旧行为。
	renewBefore := w.cfg.AvatarRenewBeforeDays
	if renewBefore < 0 {
		renewBefore = 0
	}

	// 第二步：当前佩戴的加成头像"仍然够用" → 不做任何动作（用户明确要求：
	// 不要每次登录就执行佩戴；是否需要换，以服务端数据为准）。
	//
	// 两条判据缺一不可：
	//  1. Exp > 1           —— 加成系数 = 1（无加成头像）与已过期一样视为"需要更换"
	//                         （用户 2026-09-09 确认）。不要放宽成 >= 1，
	//                         否则戴着无加成头像将永远不会被换掉。
	//  2. LeftDays > renewBefore —— 剩余不足阈值时提前换（默认剩余 ≤1 天即换）。
	// worn 记录"当前佩戴且加成有效"的条目（来源可能是 space 首页或全量列表），
	// 后面判断"提前更换是否值得"要用到它。
	worn := sp.MatchWorn()
	if worn != nil && worn.Exp > 1 && worn.LeftDays > renewBefore {
		w.setAvatarInfo(worn.GoodID, worn.Exp, worn.LeftDays, worn.Title, "")
		w.log.Info("当前佩戴的加成头像仍够用，无需更换", "good_id", worn.GoodID,
			"title", worn.Title, "exp", worn.Exp, "left", avatarLeftText(worn.LeftDays),
			"renew_before_days", renewBefore)
		return nil
	}

	// 走到这里：未佩戴 / 佩戴的无加成 / 已过期，或佩戴条目不在空间首页
	// （拥有条目超过一页）→ 翻全量列表核对并择优
	items, err := w.mall.ListDress(dressCtx, w.sess.UID, w.sess.Token, sp.WornURL)
	if err != nil {
		w.log.Warn("装扮列表获取失败", "err", err)
		w.avatarTransient("装扮列表获取失败")
		return nil
	}
	// 空列表（HTTP 成功但解析出 0 条）是商城对同 IP 并发会话的偶发异常响应
	//（实测 chouyoku 单独命中），换会话重试一次，仍为空才认输。
	if len(items) == 0 {
		w.log.Warn("装扮列表为空（疑似商城偶发异常响应），3 秒后重试")
		time.Sleep(3 * time.Second)
		retryCtx, retryCancel := context.WithTimeout(ctx, 30*time.Second)
		items, err = w.mall.ListDress(retryCtx, w.sess.UID, w.sess.Token, sp.WornURL)
		retryCancel()
		if err != nil {
			w.log.Warn("装扮列表重试失败", "err", err)
			w.avatarTransient("装扮列表获取失败")
			return nil
		}
	}
	w.log.Info("装扮列表获取完成", "count", len(items))

	// 全量列表里再核对一次佩戴状态（佩戴条目可能不在空间首页那一页）。
	// 判据同上：Exp > 1 且剩余超过阈值才算"仍然够用"。
	if full := mall.FindByURL(items, sp.WornURL); full != nil {
		worn = full // 以全量列表的数据为准（剩余天数/名称更完整）
		if full.Exp > 1 && full.LeftDays > renewBefore {
			w.setAvatarInfo(full.GoodID, full.Exp, full.LeftDays, full.Title, "")
			w.log.Info("当前佩戴的加成头像仍够用（全量列表核对），无需更换",
				"good_id", full.GoodID, "title", full.Title, "exp", full.Exp,
				"left", avatarLeftText(full.LeftDays))
			return nil
		}
	}

	// 确实需要更换：选最优佩戴。
	//
	// 【2026-10-07 事故修复】候选必须把 space 页的「我拥有的装扮」（sp.Items）算进去：
	// ListDress（POST /）返回的是**可购买的商品目录**，未拥有的条目没有有效期
	// （实测目录里 22 个加成头像的剩余天数全为 0），只用它做候选时，
	// 明明拥有 1.4 倍剩 21 天的头像（good_id=127）也会被判成"无可用加成头像"。
	// 合并两个来源并按 good_id 去重（同 ID 保留带剩余天数的那份）。
	candidates := mall.MergeDressItems(sp.Items, items)
	best := mall.PickBestDress(candidates)
	if best == nil {
		// 列出所有加成条目辅助诊断：是确实没有，还是有但已过期/条件不满足
		for _, it := range candidates {
			if it.Exp > 1 {
				w.log.Info("加成头像明细", "good_id", it.GoodID, "title", it.Title,
					"exp", it.Exp, "left", avatarLeftText(it.LeftDays),
					"game", it.GameName, "level", it.Level)
			}
		}
		w.log.Info("未找到可用的经验加成头像（无加成头像或均已过期），跳过",
			"owned", len(sp.Items), "catalog", len(items), "merged", len(candidates))
		w.setAvatarInfo(0, 0, 0, "", "无可用加成头像")
		return nil
	}

	// 边界 1（2026-10-03）：若"最优"就是当前正戴着的那个——典型情况是它即将到期，
	// 但列表里并没有更好的替代品——就不要重复佩戴。否则每次检查都会白戴一次，
	// 服务端还照常返回"装备成功"，日志看着像换了头像，实际毫无变化。
	if worn != nil && worn.GoodID == best.GoodID {
		w.setAvatarInfo(best.GoodID, best.Exp, best.LeftDays, best.Title, "")
		w.log.Info("当前头像即将到期但已是可用选项中最佳的，保持不动",
			"good_id", best.GoodID, "title", best.Title,
			"exp", best.Exp, "left", avatarLeftText(best.LeftDays))
		return nil
	}

	// 边界 2（2026-10-03 补充，用户要求）：**提前更换的条件更严格**——
	// 只有当候选「有效期更长」**且**「加成不低于当前」时才换；
	// 否则宁可把当前头像用到过期再换，避免"为了提前续期反而降了加成"
	// （例：为换成 30 天的 1.2 倍，而放弃还剩 1 天的 1.4 倍）。
	//
	// 仅约束"提前更换"这一种情形；已过期 / 未佩戴 / 无加成（worn 无效）不受此限，
	// 仍按最优直接更换——那时当前头像已无加成可言，换什么都不亏。
	earlyRenew := worn != nil && worn.Exp > 1 && worn.LeftDays > 0 && worn.LeftDays <= renewBefore
	if earlyRenew && !earlyRenewWorth(worn, best) {
		w.setAvatarInfo(worn.GoodID, worn.Exp, worn.LeftDays, worn.Title, "")
		w.log.Info("提前更换条件不满足（候选加成更低或有效期更短），暂不更换，等当前头像过期后再换",
			"current_good_id", worn.GoodID, "current_exp", worn.Exp,
			"current_left", avatarLeftText(worn.LeftDays),
			"candidate_good_id", best.GoodID, "candidate_exp", best.Exp,
			"candidate_left", avatarLeftText(best.LeftDays))
		return nil
	}

	res, err := w.mall.DressUp(dressCtx, w.sess.UID, w.sess.Token, best.GoodID)
	if err != nil {
		w.log.Warn("头像佩戴请求失败", "good_id", best.GoodID, "title", best.Title, "err", err)
		w.avatarTransient("头像佩戴请求失败")
		return nil
	}
	if !res.Success() {
		// 常见原因：该头像绑定页游角色（如「刀剑笑之霸刀 7000级」），
		// 本账号在该页游无角色或等级不足。服务端 msg 会说明具体原因。
		w.log.Warn("头像佩戴未成功（多为绑定条件不满足）", "good_id", best.GoodID,
			"title", best.Title, "num", res.Num, "msg", res.Data)
		w.setAvatarInfo(0, 0, 0, "", res.Data)
		return nil
	}

	w.setAvatarInfo(best.GoodID, best.Exp, best.LeftDays, best.Title, res.Data)
	w.log.Info("已佩戴经验加成头像", "good_id", best.GoodID, "title", best.Title,
		"exp", best.Exp, "left_days", avatarLeftText(best.LeftDays), "msg", res.Data)
	return nil
}

// setAvatarInfo 更新当前头像状态（加锁，供 buildTasks/UI 读取）
func (w *Worker) setAvatarInfo(goodID int, exp float64, left int, name, msg string) {
	w.statsMu.Lock()
	w.avatarGoodID = goodID
	w.avatarExp = exp
	w.avatarLeft = left
	w.avatarName = name
	w.avatarMsg = msg
	w.statsMu.Unlock()
}

// avatarTransient 记录一次「瞬态失败」：回滚「今天已处理」标记、安排退避重试，
// 并把原因写进 UI 状态。仅用于"没拿到结论"的失败（商城超时、页面异常），
// 不用于业务性终态（如"无可用加成头像""佩戴被服务端拒绝"——那些是结论）。
//
// 为什么必须回滚 avatarKey（2026-10-10 事故）：
//
//	avatarKey == 今天 的语义是"今天的头像检查已有结论"。但商城超时属于**没有结论**，
//	此前它照样把当天标记为已处理；若这次失败发生在进程刚启动时（首轮调用），
//	因为主循环不再调用检查，头像行会一直空白到下次重登——实测 chouyoku/wingser
//	自 10-08 16:54 起空白两天，且期间头像到期也不会自动续期。
//
// 为什么必须写 UI 状态：
//
//	avatarMsg 为空时前端 avatarHtml 直接 return ""（整行不渲染），用户看到的是
//	"这一行不见了"，而不是"出错了"。写入后 UI 显示灰色说明（悬停可见）。
func (w *Worker) avatarTransient(note string) {
	w.statsMu.Lock()
	w.avatarKey = 0 // 回滚：本次不算"今天已处理"，退避到点后重试
	backoff := w.avatarRetryBackoff * 2
	if backoff < avatarRetryMinBackoff {
		backoff = avatarRetryMinBackoff
	}
	if backoff > avatarRetryMaxBackoff {
		backoff = avatarRetryMaxBackoff
	}
	w.avatarRetryBackoff = backoff
	w.avatarRetryAt = time.Now().Add(backoff)
	w.statsMu.Unlock()

	// setAvatarInfo 内部也取 statsMu，必须在上面释放后再调用（不可重入）。
	w.setAvatarInfo(0, 0, 0, "", note+"，稍后重试")
}

// earlyRenewWorth 判断候选头像是否值得用来"提前更换"当前头像。
//
// 用户要求（2026-10-03）：提前换必须**同时**满足两条，缺一不可——
//  1. 候选有效期更长（best.LeftDays > worn.LeftDays）；
//  2. 候选加成不低于当前（best.Exp >= worn.Exp）。
//
// 否则宁可把当前头像用到过期再换，避免"为了提前续期反而降了加成"
// （典型：为换成 30 天的 1.2 倍，而放弃还剩 1 天的 1.4 倍）。
func earlyRenewWorth(worn, best *mall.DressItem) bool {
	if worn == nil || best == nil {
		return false
	}
	return best.Exp >= worn.Exp && best.LeftDays > worn.LeftDays
}

// avatarLeftText 剩余天数的展示文本（永久显示为「永久」）
func avatarLeftText(days int) string {
	if days >= mall.LeftDaysForever {
		return "永久"
	}
	return strconv.Itoa(days) + "天"
}
