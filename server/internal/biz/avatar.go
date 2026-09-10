package biz

import (
	"context"
	"strconv"
	"time"

	"youjuhang/internal/mall"
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
// 频率：主循环每轮调用但按 avatarKey（日期）去重 → 每天一次；
// 常态下每天只有一个 GET 请求（space 页），只有需要更换时才翻页拉全量列表。
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
	if !w.cfg.AvatarEnabled {
		return
	}
	// 两个条件都在 statsMu 下判断并置位，保证原子性：
	//  1. 当天已处理过（avatarKey == 今天）→ 不起；
	//  2. 已有检查在跑（avatarChecking）→ 不起，避免并发导致重复佩戴/状态抖动。
	w.statsMu.Lock()
	if w.avatarChecking || w.avatarKey == dayKey(time.Now()) {
		w.statsMu.Unlock()
		return
	}
	w.avatarChecking = true
	w.statsMu.Unlock()

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
		return nil
	}
	w.log.Info("个人空间获取完成", "count", len(sp.Items), "worn_url", sp.WornURL)

	// 第二步：当前佩戴的加成头像仍有效 → 不做任何动作（用户明确要求：
	// 不要每次登录就执行佩戴；是否需要换，以服务端数据为准）。
	//
	// 注意判据是 Exp > 1：**加成系数 = 1（无加成头像）与已过期一样视为"需要更换"**，
	// 用户 2026-09-09 确认——戴着无加成头像时应当检查并改戴加成头像。
	// 不要把它放宽成 >= 1，否则戴无加成头像将永远不会被换掉。
	if worn := sp.MatchWorn(); worn != nil && worn.Exp > 1 && worn.LeftDays > 0 {
		w.setAvatarInfo(worn.GoodID, worn.Exp, worn.LeftDays, worn.Title, "")
		w.log.Info("当前佩戴的加成头像仍有效，无需更换", "good_id", worn.GoodID,
			"title", worn.Title, "exp", worn.Exp, "left", avatarLeftText(worn.LeftDays))
		return nil
	}

	// 走到这里：未佩戴 / 佩戴的无加成 / 已过期，或佩戴条目不在空间首页
	// （拥有条目超过一页）→ 翻全量列表核对并择优
	items, err := w.mall.ListDress(dressCtx, w.sess.UID, w.sess.Token, sp.WornURL)
	if err != nil {
		w.log.Warn("装扮列表获取失败", "err", err)
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
			return nil
		}
	}
	w.log.Info("装扮列表获取完成", "count", len(items))

	// 全量列表里再核对一次佩戴状态（佩戴条目可能不在空间首页那一页）。
	// 判据同上：Exp > 1 才算"加成有效"，系数 = 1 视为需要更换。
	if worn := mall.FindByURL(items, sp.WornURL); worn != nil && worn.Exp > 1 && worn.LeftDays > 0 {
		w.setAvatarInfo(worn.GoodID, worn.Exp, worn.LeftDays, worn.Title, "")
		w.log.Info("当前佩戴的加成头像仍有效（全量列表核对），无需更换",
			"good_id", worn.GoodID, "title", worn.Title, "exp", worn.Exp,
			"left", avatarLeftText(worn.LeftDays))
		return nil
	}

	// 确实需要更换：选最优佩戴
	best := mall.PickBestDress(items)
	if best == nil {
		// 列出所有加成条目辅助诊断：是确实没有，还是有但已过期/条件不满足
		for _, it := range items {
			if it.Exp > 1 {
				w.log.Info("加成头像明细", "good_id", it.GoodID, "title", it.Title,
					"exp", it.Exp, "left", avatarLeftText(it.LeftDays),
					"game", it.GameName, "level", it.Level)
			}
		}
		w.log.Info("未找到可用的经验加成头像（无加成头像或均已过期），跳过", "count", len(items))
		w.setAvatarInfo(0, 0, 0, "", "无可用加成头像")
		return nil
	}

	res, err := w.mall.DressUp(dressCtx, w.sess.UID, w.sess.Token, best.GoodID)
	if err != nil {
		w.log.Warn("头像佩戴请求失败", "good_id", best.GoodID, "title", best.Title, "err", err)
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

// avatarLeftText 剩余天数的展示文本（永久显示为「永久」）
func avatarLeftText(days int) string {
	if days >= mall.LeftDaysForever {
		return "永久"
	}
	return strconv.Itoa(days) + "天"
}
