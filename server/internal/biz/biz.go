// Package biz 实现业务层：在线时长保持（Refresh）、任务奖励领取、每日签到、商城礼包领取
// 协议细节见 docs/protocol_notes.md
package biz

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"runtime"
	"strings"
	"sync"
	"time"

	"youjuhang/internal/config"
	"youjuhang/internal/mall"
	"youjuhang/internal/proto"
	"youjuhang/internal/protocol"
	"youjuhang/internal/room"
	"youjuhang/internal/session"
)

// rolloverIfNeeded 跨天统一重置：把全部「当日」状态清零，
// 使签到、任务领取、商城、挂机、战队在**新一天重新执行一遍**。
//
// 为什么需要统一入口：跨天判断原先散落在各功能内部（各自比 dayKey），
// 极易遗漏——实测就漏了战队捐献（ContributeStore 只在启动时 load() 判断日期，
// 长跑不重启时新一天会读到昨天的已捐量而完全不捐）。
// 集中到一处后，每轮主循环调用一次，既不会漏项，也便于在日志里确认跨天发生过。
//
// 主循环每轮调用；首次运行（prev==0）只记录日期不做重置。
func (w *Worker) rolloverIfNeeded() {
	key := dayKey(time.Now())
	w.rolloverMu.Lock()
	if w.rolloverDay == key {
		w.rolloverMu.Unlock()
		return
	}
	prev := w.rolloverDay
	w.rolloverDay = key
	w.rolloverMu.Unlock()

	if prev == 0 {
		return // 首次运行，无需重置
	}
	w.log.Info("检测到跨天，重置当日状态以执行新一天任务", "from", prev, "to", key)

	// 每日签到：允许新一天重新签到
	// 商城礼包：每日领取额度刷新
	// 这些字段会被 buildTasks 读取（可能来自房间挂机 goroutine），必须在
	// statsMu 下写，与 buildTasks 的读取快照保持同一把锁。
	w.statsMu.Lock()
	w.checkInKey = 0
	w.checkInOK = false
	w.mallKey = 0
	// 经验头像：每天重新检查。头像有效期通常 30 天且会过期，
	// 不清则第二天起不再检查，过期后加成丢失且无人察觉。
	w.avatarKey = 0
	w.statsMu.Unlock()

	// 任务领取记录：新一天的任务可以重新领取
	w.claimMu.Lock()
	w.claimedDay = 0
	w.claimedToday = make(map[uint64]bool)
	w.claimMu.Unlock()

	// 战队状态缓存：新一天重新检测（战队可能变动）
	w.gangMu.Lock()
	w.gangCheckedDay = 0
	w.gangOK = false
	w.gangMu.Unlock()

	// 房间挂机：清掉「当日 3 小时目标已完成」标记，新一天自动重新挂机。
	// 不清的话 maybeStartRoomHang 会一直跳过，第二天整天不挂机。
	w.roomMu.Lock()
	w.hangDoneKey = 0
	w.roomMu.Unlock()

	// 战队捐献：清空当日已捐量，并让持久化 store 跨天作废昨日记录
	w.statsMu.Lock()
	w.contributeToday = 0
	w.statsMu.Unlock()
	if w.stateStore != nil {
		w.stateStore.ResetContributeIfNewDay()
	}
}

// ErrSessionExpired 会话失效（需要重新登录）
var ErrSessionExpired = errors.New("session expired")

// recoverGoroutine 捕获子 goroutine 的 panic，避免整个进程被终结。
// 必须以 `defer recoverGoroutine(w.log, "名称")` 的形式挂在每个长期运行的子 goroutine 上。
//
// 为什么必须有这个（2026-08-31 线上崩溃事故）：
//  1. main() 里的 `defer recoverPanic()` 只能捕获**主 goroutine** 的 panic。
//     任何子 goroutine 未捕获的 panic 都会直接终结整个进程，recover 拿不到。
//  2. 本程序用 `-H windowsgui` 构建（无控制台窗口），panic 输出写往 stderr 被丢弃，
//     日志文件里**不留任何痕迹**。
//  3. 外部表现就是「进程无声消失、Web 控制台连不上（HTTP 0）」，无法定位。
//
// 挂上本函数后，panic 会被记录到账号日志并带完整调用栈，进程继续运行，
// 主循环下一轮还会自动重启挂机会话。
func recoverGoroutine(log *slog.Logger, name string) {
	if r := recover(); r != nil {
		buf := make([]byte, 16384)
		n := runtime.Stack(buf, false)
		log.Error("子 goroutine panic 已捕获，进程继续运行",
			"goroutine", name, "panic", r, "stack", string(buf[:n]))
	}
}

// 任务完成状态
const (
	TaskDone      = "done"      // 已完成
	TaskClaimable = "claimable" // 已达成待领取
	TaskDoing     = "doing"     // 进行中（未达成）
	TaskPending   = "pending"   // 待处理（未签到 / 待领取）
	TaskNotDue    = "not_due"   // 未到时间（如礼包领取期外）
	TaskDisabled  = "disabled"  // 功能未启用
	TaskUnknown   = "unknown"   // 未知
)

// TaskStatus 是单个任务的完成状态（UI 展示用）
type TaskStatus struct {
	ID       uint64 `json:"id"`
	Name     string `json:"name"`
	State    string `json:"state"`
	Progress uint64 `json:"progress"`          // 进度（任务 f2，单位视任务而定）
	Detail   string `json:"detail,omitempty"`  // 展示用进度文本，如 "19/21 天"
}

// UserStats 是用户等级/经验/游币状态（来自 UserInfo 514 / RefreshInfo 515）
type UserStats struct {
	Level      uint64 // 等级（514: 属性 id=1）
	Exp        uint64 // 总经验（515: 属性 id=10000）
	ExpToday   uint64 // 当日累计获得经验（程序统计：总经验-当日基线，跨天重置）
	Gold       uint64 // 游币（515: 属性 id=15）
	Nick       string // 昵称（514: 属性 id=20000）
	// LevelStart 当前等级的起始累计经验（514: 属性 id=10001）
	// LevelNext 下一等级的起始累计经验（514: 属性 id=10002）
	//
	// 【重要更正 2026-09-02，抓包 + 客户端逆向实证】
	// 这两个字段 **不是**「已获得 / 升级所需」，而是游戏内置等级经验表的
	// **相邻两项**（X-Zone.exe 中 off=0xbd0fd0 起的 1501 项 uint32 递增数组）：
	//
	//	10001 = 表[等级-1]（当前等级起始）
	//	10002 = 表[等级]  （下一等级起始）
	//
	// 三个账号交叉验证，表索引完全吻合：
	//	赛博大善人 Lv9  → 10001=1040(表[8])      10002=1260(表[9])
	//	chouyoku  Lv178 → 10001=1971612(表[177]) 10002=1994496(表[178])
	//	wingser   Lv184 → 10001=2110896(表[183]) 10002=2134572(表[184])
	//
	// 因此它们是**静态值**，本就不随经验增长而变化（不是服务器"不刷新"）。
	// 真正的升级进度必须用总经验 Exp(10000) 推算，见 LevelGot / LevelTotal。
	// 旧实现直接用 10001/10002 当分子分母，导致百分比数值错误且长期不变。
	LevelStart uint64 // 当前等级的起始累计经验（514: 属性 id=10001）
	LevelNext  uint64 // 下一等级的起始累计经验（514: 属性 id=10002）
	Tasks      []TaskStatus // 个人任务（签到/在线/连续签到/礼包）
	TeamTasks  []TaskStatus // 战队任务（战盟挂机/战队捐献），仅已加入战队的账号开启
	// Avatar 经验头像状态（2026-09-09 起**不再作为任务**展示——佩戴头像不是"做任务"，
	// 放在任务徽章里语义错误；改为随状态推送，由前端渲染在「基础信息」列）。
	Avatar AvatarInfo
}

// AvatarInfo 经验头像的当前状态（基础信息列展示用）
type AvatarInfo struct {
	Worn bool    `json:"worn"`           // 是否已佩戴加成头像
	Name string  `json:"name,omitempty"` // 头像名称，如「铁血士兵」
	Exp  float64 `json:"exp,omitempty"`  // 经验加成倍数，如 1.4
	Left int     `json:"left,omitempty"` // 剩余天数；mall.LeftDaysForever(9999) 表示永久
	Note string  `json:"note,omitempty"` // 未佩戴时的说明（功能未启用/无可用/失败原因）
}

// expScale 总经验字段（10000）相对等级体系经验的倍率。
//
// 实证：赛博大善人总经验 120527、10001=1040 → (120527-104000)/100 = 165，
// 与客户端显示的 165 完全一致，故倍率为 100。
const expScale = 100

// LevelGot 返回当前等级内**已获得**的经验（等级体系单位）。
//
// 见 LevelStart 的字段说明：进度必须由总经验推算，不能用 10001 自身。
func (s UserStats) LevelGot() uint64 {
	base := s.LevelStart * expScale
	if s.Exp <= base {
		return 0
	}
	return (s.Exp - base) / expScale
}

// LevelTotal 返回升到下一级所需的**总**经验（等级体系单位），即 10002-10001。
func (s UserStats) LevelTotal() uint64 {
	if s.LevelNext <= s.LevelStart {
		return 0
	}
	return s.LevelNext - s.LevelStart
}

// StatsObserver 是用户状态变化回调（供 UI 展示）
type StatsObserver func(stats UserStats)

// Worker 是单账号业务执行器
type Worker struct {
	sess *session.Session
	cfg  *config.Config
	log  *slog.Logger

	mall      *mall.Client
	room      *room.Client

	// stateStore 统一持久化：当日捐献量 + 月度礼包领取状态（按 uid 区分）。
	// 单实例互斥锁保护并发写，详见 state_store.go。
	stateStore *StateStore

	stats        UserStats // 最近一次查询的用户状态
	expBase      uint64    // 当日经验基线（当天第一次刷新时的总经验）
	expBaseDay   string    // 基线对应的日期（2006-01-02），空表示未初始化
	onStats      StatsObserver

	tasks        []Mission // 最近一次任务列表快照（任务状态展示用）
	checkInKey   int       // 最近一次执行签到的日期 key（年*10000+月*100+日）
	checkInOK    bool      // 最近一次签到是否成功（ret==1）
	mallKey      int       // 最近一次处理商城领取的日期 key
	mallOKKey    int       // 最近一次商城领取成功的月份 key（年*100+月）

	// 头像装扮：自动佩戴经验加成最高的头像，详见 avatar.go。
	// 字段均由 statsMu 保护（buildTasks 会在挂机 goroutine 中读取）。
	avatarKey      int  // 最近一次处理头像的日期 key（当天只处理一次）
	// avatarChecking 是否正在异步检查头像（2026-09-10 并发防护）。
	// 异步后存在这样的竞态：goroutine 仍在跑（慢网络）时跨天，
	// rolloverIfNeeded 把 avatarKey 清零 → 主循环以为"今天没查过"又起一个
	// goroutine，两个并发执行可能先后佩戴不同头像，导致状态抖动。
	// 用本标志确保同一时刻只有一个检查在跑。
	avatarChecking bool
	avatarGoodID int     // 当前佩戴的头像商品 ID（0=未知/未佩戴）
	avatarExp    float64 // 当前佩戴头像的经验加成倍数（0=未知）
	avatarLeft   int     // 当前佩戴头像剩余天数（mall.LeftDaysForever=永久）
	avatarName   string  // 当前佩戴头像名称
	avatarMsg    string  // 最近一次处理结果的说明（UI 展示用）
	claimedDay   int       // claimedToday 对应的日期
	claimedToday map[uint64]bool

	// statsMu 保护 tasks 与 stats。
	//
	// 2026-08-31 崩溃根因：w.tasks 由主循环在 claimMu 内写（checkAndClaim），
	// 却被 buildTasks 在**无锁**状态下读；而 buildTasks 经 notifyStats 会被
	// 房间挂机 goroutine 调用（挂机退出/启动各一次）。挂机退出恰好与主循环的
	// 任务刷新撞在同一秒，读到撕裂的 slice 头 → panic。
	// 该 panic 发生在子 goroutine，main() 的 defer recoverPanic() 兜不住；
	// 又因 -H windowsgui 无控制台、stderr 被丢弃，日志里不留任何痕迹，
	// 表现为「进程无声消失」。故必须用锁消除竞争，并为子 goroutine 加 recover。
	statsMu sync.Mutex

	roomMu      sync.Mutex          // 房间挂机状态锁
	roomOn      bool                // 房间挂机是否运行中
	roomSince   time.Time           // 本次挂机会话开始时间
	roomErr     error               // 最近一次挂机错误（nil=正常）
	roomZone    uint32              // 挂机区 ID
	roomCancel  context.CancelFunc  // 挂机监督主动退出：取消挂机子上下文
	hangDoneKey int                 // 挂机目标完成日期 key（年*10000+月*100+日），当日不再启动挂机
	claimMu     sync.Mutex          // 任务领取串行化（主循环与挂机监督共用）

	gangMu         sync.Mutex // 战队状态缓存锁
	gangOK         bool       // 账号是否已加入战队（由 596/f3=3 战队详情查询判定，当天缓存）
	gangCheckedDay int        // 战队状态已检测的日期 key（0=未检测）

	contributeToday int // 当日已捐献量（启动时从 StateStore 读取；运行时更新）

	rolloverMu  sync.Mutex // 跨天重置串行化
	rolloverDay int        // 上次观察到的日期 key（0=首次运行）
}

// NewWorker 创建业务执行器
func NewWorker(sess *session.Session, cfg *config.Config, log *slog.Logger, stateStore *StateStore) *Worker {
	return &Worker{
		sess:         sess,
		cfg:          cfg,
		log:          log,
		mall:         mall.New(),
		stateStore:   stateStore,
		claimedToday: make(map[uint64]bool),
		// 保留旧字段供 buildTasks 等地方使用，值与 StateStore 同步
		contributeToday: stateStore.ContributeToday(sess.UID),
	}
}

// SetStatsObserver 注册用户状态变化回调（UI 展示用）
func (w *Worker) SetStatsObserver(fn StatsObserver) {
	w.onStats = fn
}

// notifyStats 构建任务状态并推送当前状态。
// 本函数会被主循环、maybeStartRoomHang 以及**房间挂机 goroutine** 调用，
// 因此内部所有共享状态访问都必须加锁。
// 回调在锁外调用：onStats 由 core 层实现，会去拿 core 的锁，
// 持锁回调有死锁风险；这里先复制一份快照再出锁。
func (w *Worker) notifyStats() {
	w.buildTasks()
	if w.onStats == nil {
		return
	}
	w.statsMu.Lock()
	snap := w.stats
	w.statsMu.Unlock()
	w.onStats(snap)
}

// Run 运行主循环：刷新在线 → 领取任务 → 签到 → 商城
func (w *Worker) Run(ctx context.Context) error {
	interval := w.refreshInterval()
	w.log.Info("账号业务启动", "uid", w.sess.UID, "gate", w.sess.GateAddr, "refresh_min", interval.Minutes())

	// 启动立即查询用户信息（等级/昵称）并执行一轮业务
	if err := w.queryAndLogStats(ctx); err != nil && !errors.Is(err, ErrSessionExpired) {
		w.log.Warn("用户信息查询失败", "err", err)
	}
	// 首轮检测战队状态（战队任务是否开启），供挂机/捐献及 UI 展示使用
	w.hasGang(ctx)
	if err := w.refreshAndClaim(ctx); err != nil {
		w.log.Warn("首轮刷新失败", "err", err)
		if errors.Is(err, ErrSessionExpired) {
			return err
		}
	}
	if err := w.maybeCheckIn(ctx); err != nil && !errors.Is(err, ErrSessionExpired) {
		w.log.Warn("签到异常", "err", err)
	}
	if err := w.maybeMallClaim(ctx); err != nil && !errors.Is(err, ErrSessionExpired) {
		w.log.Warn("商城领取异常", "err", err)
	}
	// 头像检查异步执行：它要访问商城（慢网络下 3~4 秒），同步会推迟后面的
	// 捐献、房间挂机启动与状态推送，表现为「登录后数据加载慢」。
	// 结果出来后由 goroutine 内部单独推送状态，UI 头像行随后填充。
	w.startAvatarCheck(ctx)
	if err := w.maybeContribute(ctx); err != nil && !errors.Is(err, ErrSessionExpired) {
		w.log.Warn("贡献捐献异常", "err", err)
	}
	// 首轮业务验证会话有效后启动房间挂机（战盟挂机任务）
	w.maybeStartRoomHang(ctx)
	w.notifyStats() // 首轮业务（任务/签到/商城/挂机）后推送最新任务状态

	round := 0
	for {
		wait := w.jitter(interval)
		w.log.Debug("等待下轮", "wait", wait.Round(time.Second))
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(wait):
		}
		// 跨天检测放在每轮业务之前：新一天先把签到/任务/商城/挂机/捐献的
		// 「当日已完成」标记全部清零，保证新一天的任务都会被执行。
		w.rolloverIfNeeded()
		if err := w.refreshAndClaim(ctx); err != nil {
			w.log.Warn("刷新失败", "err", err)
			if errors.Is(err, ErrSessionExpired) {
				return err
			}
		}
		if err := w.maybeCheckIn(ctx); err != nil && !errors.Is(err, ErrSessionExpired) {
			w.log.Warn("签到异常", "err", err)
		}
		if err := w.maybeMallClaim(ctx); err != nil && !errors.Is(err, ErrSessionExpired) {
			w.log.Warn("商城领取异常", "err", err)
		}
		if err := w.maybeContribute(ctx); err != nil && !errors.Is(err, ErrSessionExpired) {
			w.log.Warn("贡献捐献异常", "err", err)
		}
		// 房间挂机掉线时自动重启
		w.maybeStartRoomHang(ctx)
		w.notifyStats() // 本轮业务后推送最新任务状态
		round++
		if round%12 == 0 {
			w.logStats() // 约每 60 分钟输出一次状态摘要
			// 约每 60 分钟刷新一次升级进度（10001/10002 只在 UserInfo 514 下发）。
			if err := w.queryUserInfo(ctx); err != nil {
				if errors.Is(err, ErrSessionExpired) {
					return err
				}
				w.log.Warn("用户信息刷新失败", "err", err)
			}
		}
	}
}

// maybeStartRoomHang 在未运行房间挂机时启动挂机会话（进入"8bit音乐(挂机区)"保持在线）。
// 挂机会话独立 goroutine 运行，掉线后由主循环每轮自动重启；
// 挂机监督 goroutine 定期查询战队任务（1172），3 小时在线奖励领取完成后自动退出挂机。
// 仅已加入战队的账号开启挂机（战盟挂机任务），未加入战队默认不开启。
func (w *Worker) maybeStartRoomHang(ctx context.Context) {
	if !w.cfg.RoomHangEnabled {
		return
	}
	if !w.hasGang(ctx) {
		w.log.Debug("账号未加入战队，不启动战盟挂机")
		return
	}
	w.roomMu.Lock()
	if w.roomOn {
		w.roomMu.Unlock()
		return
	}
	if w.hangDoneKey == dayKey(time.Now()) {
		w.roomMu.Unlock()
		return // 当日挂机目标已完成（3小时奖励已领取），不再启动
	}
	if w.room == nil {
		w.room = room.New(w.sess.Gate, w.log)
	}
	w.roomOn = true
	w.roomSince = time.Now()
	w.roomErr = nil
	// 区 ID 固定为音乐挂机区（room.HangZoneID=1，自由区/含 fc_8bit），不提供配置开关
	w.roomZone = room.HangZoneID
	hangCtx, hangCancel := context.WithCancel(ctx)
	w.roomCancel = hangCancel
	w.roomMu.Unlock()

	w.log.Info("启动房间挂机", "zone", w.roomZone, "addr", w.sess.GateAddr)
	go func() {
		defer recoverGoroutine(w.log, "roomHang")
		err := w.room.Hang(hangCtx, w.sess.GateAddr, w.sess.UID, w.sess.Token, w.roomZone)
		w.roomMu.Lock()
		w.roomOn = false
		w.roomErr = err
		w.roomMu.Unlock()
		if err != nil && !errors.Is(err, context.Canceled) {
			w.log.Warn("房间挂机会话结束", "err", err)
		} else {
			w.log.Info("房间挂机会话结束")
		}
		w.notifyStats()
	}()
	go w.hangSupervisor(hangCtx)
	w.notifyStats()
}

// hangSupervisor 挂机监督：定期查询战队任务（1172）并领取可领取奖励，
// 检测到 3 小时在线奖励已达成并领取后自动退出挂机（取消挂机子上下文，触发离房）。
// 节奏：未满 3 小时每 5 分钟检查一次；超过 3 小时仍未达成时每 10 分钟复查一次。
func (w *Worker) hangSupervisor(hangCtx context.Context) {
	defer recoverGoroutine(w.log, "hangSupervisor")
	const (
		goal      = 3 * time.Hour
		checkFast = 5 * time.Minute
		checkSlow = 10 * time.Minute
	)
	w.log.Info("挂机监督启动", "goal", goal.String())
	t := time.NewTimer(checkFast)
	defer t.Stop()
	for {
		select {
		case <-hangCtx.Done():
			w.log.Info("挂机监督结束")
			return
		case <-t.C:
		}
		if !w.isRoomOn() {
			return // 挂机已停止（会话中断等），无需继续监督
		}
		missions, err := w.hangCheckAndClaim(hangCtx)
		if err != nil {
			if errors.Is(err, ErrSessionExpired) {
				w.log.Warn("挂机监督：会话失效，退出挂机")
				w.cancelHang()
				return
			}
			w.log.Warn("挂机监督：任务检查异常", "err", err)
			t.Reset(checkSlow)
			continue
		}
		if hangGoalDone(missions) {
			w.log.Info("挂机目标已达成（3小时在线奖励已领取），自动退出挂机")
			w.roomMu.Lock()
			w.hangDoneKey = dayKey(time.Now())
			w.roomMu.Unlock()
			w.cancelHang()
			return
		}
		// roomSince 由 maybeStartRoomHang 在 roomMu 下写入，读取必须同锁，
		// 否则构成 time.Time 的数据竞争（撕裂读可能得到非法 loc 指针）。
		w.roomMu.Lock()
		since := w.roomSince
		w.roomMu.Unlock()
		if time.Since(since) >= goal {
			t.Reset(checkSlow) // 超过 3 小时：每 10 分钟复查
		} else {
			t.Reset(checkFast)
		}
	}
}

// isRoomOn 报告房间挂机是否运行中
func (w *Worker) isRoomOn() bool {
	w.roomMu.Lock()
	defer w.roomMu.Unlock()
	return w.roomOn
}

// cancelHang 取消挂机子上下文，触发 room.Hang 离房返回
func (w *Worker) cancelHang() {
	w.roomMu.Lock()
	defer w.roomMu.Unlock()
	if w.roomCancel != nil {
		w.roomCancel()
		w.roomCancel = nil
	}
}

// hangGoalDone 判定 3 小时在线奖励是否已达成（挂机自动退出条件）。
// 3 小时档任务判定：在线任务 ID 映射为 10800s（2154）或进度字段 f2==10800 均认可，
// 避免依赖尚未实测确认的具体任务 ID。
// 达成(f3=1) 即视为完成：实测战队界面任务达成后奖励自动发放（无需/不可手动领取），
// 服务器不因客户端领取而设置 f5，故不再要求 f5!=0（奖励由 hangCheckAndClaim 自动领取兜底）。
func hangGoalDone(missions []Mission) bool {
	for _, m := range missions {
		if !m.HasState {
			continue
		}
		if m.Field2 == 10800 || onlineMissionTarget(m.ID) == 10800 {
			return m.Field3 == 1
		}
	}
	return false
}

// hangCheckAndClaim 挂机监督专用：查询任务列表并领取可领取奖励，返回最新任务列表。
// 与 checkAndClaim 共用 claimMu，避免与主循环并发领取导致重复提交。
func (w *Worker) hangCheckAndClaim(ctx context.Context) ([]Mission, error) {
	w.claimMu.Lock()
	defer w.claimMu.Unlock()
	missions, err := w.listMissions(ctx)
	if err != nil {
		return nil, err
	}
	if err := w.claimLocked(ctx, missions); err != nil {
		return nil, err
	}
	return missions, nil
}

// refreshInterval 在线保持间隔（默认 10 分钟；UI 不提供配置，改 yaml 可调整）
func (w *Worker) refreshInterval() time.Duration {
	m := w.cfg.RefreshMinutes
	if m <= 0 {
		m = 10
	}
	return time.Duration(m) * time.Minute
}

// jitter 在 base 基础上加 ±20% 随机抖动（风控规避）
func (w *Worker) jitter(base time.Duration) time.Duration {
	f := 0.8 + rand.Float64()*0.4
	return time.Duration(float64(base) * f)
}

// refreshAndClaim 刷新在线并检查任务
func (w *Worker) refreshAndClaim(ctx context.Context) error {
	if err := w.refresh(ctx); err != nil {
		return err
	}
	if w.cfg.AutoClaim {
		if err := w.checkAndClaim(ctx); err != nil {
			w.log.Warn("任务检查异常", "err", err)
			// 会话失效（被其他设备顶下线）必须向上传播，触发"在线侦查/等待恢复"
			if errors.Is(err, ErrSessionExpired) {
				return err
			}
		}
	}
	return nil
}

// refresh 发送 RefreshInfoCMsg(515, action=5) 保持在线并解析经验/游币
func (w *Worker) refresh(ctx context.Context) error {
	body := protocol.BuildRefreshInfo(w.sess.UID, w.sess.Token, 5)
	resp, err := w.sess.Gate.Game(ctx, w.sess.GateAddr, protocol.RefreshInfoCMsg, body)
	if err != nil {
		return classify(err)
	}
	w.parseStats(resp)
	w.log.Debug("在线刷新成功", "uid", w.sess.UID)
	return nil
}

// queryUserInfo 查询 UserInfo(514) 并更新等级/昵称/升级进度，供主循环周期性调用。
//
// 属性 id 对照（2026-09-02 抓包 + 客户端逆向修正，详见 UserStats 字段说明）：
//   id=1     等级（值在 f2）
//   id=20000 昵称（值在 f4）
//   id=10000 累计总经验（值在 f3；不直接写入 Exp——
//            Exp 与当日经验基线由 RefreshInfo(515) 的 parseStats 统一维护，
//            在此覆盖会污染「当日累计经验」的计算，故只取升级进度相关字段）
//   id=10001 当前等级起始累计经验（值在 f3）
//   id=10002 下一等级起始累计经验（值在 f3）
//
// 实测样例：
//	赛博大善人 Lv9  → 10001=1040    / 10002=1260    ；总经验 120527
//	                  → 已获得 (120527-1040*100)/100 = 165，总需 1260-1040 = 220
//	                  （与游戏客户端显示的 165/220 完全一致）
//	chouyoku  Lv178 → 10001=1971612 / 10002=1994496
//	wingser   Lv184 → 10001=2110896 / 10002=2134572
func (w *Worker) queryUserInfo(ctx context.Context) error {
	body := protocol.FieldsWithAuth(w.sess.UID, w.sess.Token)
	resp, err := w.sess.Gate.Game(ctx, w.sess.GateAddr, protocol.UserInfoCMsg, body)
	if err != nil {
		return classify(err)
	}
	fields := proto.Parse(resp)
	if ret := proto.GetVarint(fields, 1); ret != 1 {
		w.log.Debug("UserInfo ret", "ret", ret, "resp", hexBrief(resp))
	}
	for _, f := range proto.GetAll(fields, 3) {
		if f.Wire != 2 {
			continue
		}
		// f3 是属性列表，f1 重复字段是单个属性（嵌套 message）
		for _, prop := range proto.GetAll(proto.Parse(f.B), 1) {
			if prop.Wire != 2 {
				continue
			}
			ap := proto.Parse(prop.B)
			switch proto.GetVarint(ap, 1) {
			case 1:
				w.statsMu.Lock()
				w.stats.Level = proto.GetVarint(ap, 2)
				w.statsMu.Unlock()
			case 20000:
				w.statsMu.Lock()
				w.stats.Nick = proto.GetString(ap, 4)
				w.statsMu.Unlock()
			case 10001:
				w.statsMu.Lock()
				w.stats.LevelStart = proto.GetVarint(ap, 3)
				w.statsMu.Unlock()
			case 10002:
				w.statsMu.Lock()
				w.stats.LevelNext = proto.GetVarint(ap, 3)
				w.statsMu.Unlock()
			case 10000:
				// 累计总经验（值在 f3）。514 也携带此字段，但 Exp 与「当日经验基线」
				// 由 RefreshInfo(515) 的 parseStats 统一维护（避免污染跨天重置逻辑）。
				// 此处仅作首次填充：Exp==0 时从 514 取值，保证 UI 首次渲染不显示「经验 0」。
				// 后续 515 的 parseStats 会用更大的值覆盖（取 max），不影响准确性。
				v := proto.GetVarint(ap, 3)
				if v > 0 {
					w.statsMu.Lock()
					if w.stats.Exp == 0 {
						w.stats.Exp = v
					}
					w.statsMu.Unlock()
				}
			}
		}
	}
	w.notifyStats()
	return nil
}

// queryAndLogStats 登录后首次查询用户信息，并输出摘要日志
func (w *Worker) queryAndLogStats(ctx context.Context) error {
	if err := w.queryUserInfo(ctx); err != nil {
		return err
	}
	w.statsMu.Lock()
	s := w.stats
	w.statsMu.Unlock()
	w.log.Info("用户信息",
		"nick", s.Nick, "level", s.Level,
		"exp", s.Exp, "gold", s.Gold,
		"lv_got", s.LevelGot(), "lv_need", s.LevelTotal())
	return nil
}

// parseStats 解析 RefreshInfo 响应中的经验/游币并记录变化
// 当日经验由程序自己累计（总经验-当日基线），跨天自动重置，
// 多次登录/被顶重登后仍按当天累计，不再透传服务器"本次登录经验"。
func (w *Worker) parseStats(resp []byte) {
	fields := proto.Parse(resp)
	var exp, gold uint64
	for _, f := range proto.GetAll(fields, 100) {
		if f.Wire != 2 {
			continue
		}
		for _, af := range proto.GetAll(proto.Parse(f.B), 1) {
			if af.Wire != 2 {
				continue
			}
			ap := proto.Parse(af.B)
			id := proto.GetVarint(ap, 1)
			// 属性值存于 f2 或 f3（按属性类型），取非零者
			v := proto.GetVarint(ap, 2)
			if v == 0 {
				v = proto.GetVarint(ap, 3)
			}
			switch id {
			case 10000:
				// 经验只增不减：取最大值，防止响应中属性块顺序导致 0 覆盖真实值
				if v > exp {
					exp = v
				}
			case 15:
				if v > 0 {
					gold = v
				}
			}
		}
	}
	if exp == 0 && gold == 0 {
		return
	}
	// 经验属性缺失或为 0（如首轮刷新响应不完整）：仅更新游币，
	// 不动总经验/当日经验/基线，避免把当日基线污染成 0。
	if exp == 0 {
		w.statsMu.Lock()
		w.stats.Gold = gold
		w.statsMu.Unlock()
		w.notifyStats()
		return
	}
	day := time.Now().Format("2006-01-02")
	crossed := day != w.expBaseDay
	if crossed {
		// 首次记录或跨天：重置当日经验基线，当日累计从 0 开始
		w.expBase = exp
		w.expBaseDay = day
	}
	// 当日累计经验 = 总经验 - 当日基线
	daily := uint64(0)
	if exp >= w.expBase {
		daily = exp - w.expBase
	}
	w.statsMu.Lock()
	oldExp, oldLevel := w.stats.Exp, w.stats.Level
	w.statsMu.Unlock()
	if oldExp != 0 && exp != oldExp && !crossed {
		w.log.Info("经验增长",
			"level", oldLevel,
			"exp_before", oldExp, "exp_now", exp,
			"exp_gain", exp-oldExp,
			"exp_today", daily, "gold", gold)
	}
	w.statsMu.Lock()
	w.stats.Exp = exp
	w.stats.ExpToday = daily
	w.stats.Gold = gold
	w.statsMu.Unlock()
	w.notifyStats()
}

// logStats 输出当前状态摘要（供日志周期展示）
func (w *Worker) logStats() {
	w.statsMu.Lock()
	s := w.stats
	w.statsMu.Unlock()
	if s.Level == 0 && s.Exp == 0 {
		return
	}
	w.log.Info("当前状态",
		"nick", s.Nick, "level", s.Level,
		"exp", s.Exp, "exp_today", s.ExpToday,
		"gold", s.Gold)
}

// Mission 是任务列表中的单个任务
type Mission struct {
	ID       uint64
	Field2   uint64 // 目标/进度
	Field3   uint64 // 状态
	Field5   uint64 // 完成时间戳
	HasState bool
	Raw      []byte
	Extra    map[uint64]uint64 // 额外字段（调试用）
}

// listMissions 获取任务列表（MissionListCMsg 1172）
func (w *Worker) listMissions(ctx context.Context) ([]Mission, error) {
	body := protocol.BuildMissionList(w.sess.UID, w.sess.Token)
	resp, err := w.sess.Gate.Game(ctx, w.sess.GateAddr, protocol.MissionListCMsg, body)
	if err != nil {
		return nil, classify(err)
	}
	// 注：此处曾无条件把原始响应写成 logs/mission_dump_<uid>_<时间>.bin（调试残留）。
	// 每次任务查询都会写一个文件，两个账号约每 60 秒各一次 → 一天近 3000 个文件、
	// 长期运行在热路径上做无谓磁盘 I/O。已移除：任务字段解析不了时用抓包分析
	// （见 .codebuddy/skills/protocol-capture-reverse），不必靠落盘原始包。
	fields := proto.Parse(resp)
	if ret := proto.GetVarint(fields, 1); ret != 1 {
		// ret 为负数（如被其他设备登录后 token 失效，ret=-10）时归类为会话失效
		return nil, classify(fmt.Errorf("mission list ret=%d", ret))
	}
	var missions []Mission
	for _, f := range proto.GetAll(fields, 3) {
		if f.Wire != 2 {
			continue
		}
		mf := proto.Parse(f.B)
		m := Mission{
			ID:     proto.GetVarint(mf, 1),
			Field2: proto.GetVarint(mf, 2),
			Field3: proto.GetVarint(mf, 3),
			Field5: proto.GetVarint(mf, 5),
			Raw:    append([]byte(nil), f.B...),
		}
		if f2 := proto.Get(mf, 2); f2 != nil {
			m.HasState = true
		}
		// 提取所有额外字段用于调试
		for _, ef := range mf {
			if ef.Num == 1 || ef.Num == 2 || ef.Num == 3 || ef.Num == 5 {
				continue
			}
			if m.Extra == nil {
				m.Extra = make(map[uint64]uint64)
			}
			m.Extra[uint64(ef.Num)] = ef.V
		}
		missions = append(missions, m)
	}
	return missions, nil
}

// checkAndClaim 检查任务列表并领取所有可领取的奖励
// 可领取判定：任务有状态（f2 存在）且 f3=1（达成）且 f5=0（未领）。
// 覆盖：在线时长任务（2150-2155）、签到天数奖励（2140=7天/2141=14天/2142=21天）等。
func (w *Worker) checkAndClaim(ctx context.Context) error {
	w.claimMu.Lock()
	defer w.claimMu.Unlock()
	missions, err := w.listMissions(ctx)
	if err != nil {
		return err
	}
	// w.tasks 会被 buildTasks 在另外的 goroutine 读取（房间挂机退出时 notifyStats），
	// 必须在 statsMu 保护下替换，否则并发读写 slice 头会撕裂导致 panic。
	w.statsMu.Lock()
	w.tasks = missions
	w.statsMu.Unlock()
	w.logMissionList(missions)
	return w.claimLocked(ctx, missions)
}

// logMissionList 记录任务列表（便于对照游戏界面确认战队任务 ID→名称映射）
func (w *Worker) logMissionList(missions []Mission) {
	if len(missions) == 0 {
		return
	}
	w.log.Info("任务列表刷新", "count", len(missions))
	for _, m := range missions {
		w.log.Info("  任务", "id", m.ID, "f2", m.Field2, "f3", m.Field3, "f5", m.Field5, "extra", m.Extra)
	}
}

// claimLocked 领取给定任务列表中所有可领取的奖励（调用方需持有 claimMu）
func (w *Worker) claimLocked(ctx context.Context, missions []Mission) error {
	if len(missions) == 0 {
		w.log.Debug("任务列表为空")
		return nil
	}

	// 跨天重置当日领取记录
	day := time.Now().Day()
	if day != w.claimedDay {
		w.claimedDay = day
		w.claimedToday = make(map[uint64]bool)
	}

	// 收集可领取任务
	var toClaim []Mission
	for _, m := range missions {
		if m.HasState && m.Field3 == 1 && m.Field5 == 0 {
			toClaim = append(toClaim, m)
		}
	}
	if len(toClaim) == 0 {
		w.log.Debug("无可领取任务")
		return nil
	}
	w.log.Info("可领取任务", "count", len(toClaim), "ids", missionIDs(toClaim))

	for _, m := range toClaim {
		if w.claimedToday[m.ID] {
			continue
		}
		w.log.Info("尝试领取任务",
			"id", m.ID, "f2", m.Field2, "f3", m.Field3, "f5", m.Field5)
		res, err := w.submitOne(ctx, m.ID, int32(day))
		if err != nil {
			if errors.Is(err, ErrSessionExpired) {
				return err
			}
			w.log.Warn("任务领取失败", "id", m.ID, "err", err)
			continue
		}
		if res.Success {
			w.claimedToday[m.ID] = true
			w.log.Info("任务领取成功", "id", m.ID)
		} else if res.AlreadyFinish {
			// 已完成不可再领：当日不再重试
			w.claimedToday[m.ID] = true
			w.log.Info("任务已完成跳过", "id", m.ID, "msg", res.Msg)
		} else {
			// 被拒（如在线时长未达标）：不标记，下轮达成后再试
			w.log.Warn("任务领取被拒", "id", m.ID, "ret", res.Ret, "msg", res.Msg)
		}
		// 逐条间隔一小段随机延时，避免突发（风控规避）
		if !sleepCtx(ctx, 150*time.Millisecond+time.Duration(rand.Int63n(200))*time.Millisecond) {
			return ctx.Err()
		}
	}
	return nil
}

// maybeMallClaim 商城礼包领取：每月 1-7 日领取微信绑定礼包（id=1）。
//
// 语义（2026-09-01 修正）：**每月一次**，领取完成后本月一直是"已完成"，
// 直到下个月才能领下一次。因此：
//  1. 状态按 uid + 月份持久化到 MallStore，进程重启不丢失；
//  2. 本月已领取（或服务器确认"已领取过"）→ 本月内不再请求接口；
//  3. 本月被判定不可领（如未绑定微信）→ 本月内同样不再请求，避免无效请求。
func (w *Worker) maybeMallClaim(ctx context.Context) error {
	if !w.cfg.MallEnabled {
		return nil
	}
	now := time.Now()
	if now.Day() < 1 || now.Day() > 7 {
		return nil // 不在领取期（每月 1-7 日）
	}
	key := dayKey(now)
	if key == w.mallKey {
		return nil // 当日已处理
	}
	w.statsMu.Lock()
	w.mallKey = key
	w.statsMu.Unlock()

	month := monthKey(now)
	// 关键：先看持久化状态。本月已领/不可领就直接跳过，不再请求接口。
	if w.stateStore != nil {
		if w.stateStore.MallIsClaimed(w.sess.UID, month) {
			w.log.Debug("商城礼包本月已领取，跳过", "month", month)
			return nil
		}
		if w.stateStore.MallIsUnavailable(w.sess.UID, month) {
			w.log.Debug("商城礼包本月不可领取（如未绑定微信），跳过",
				"month", month, "info", w.stateStore.MallLastInfo(w.sess.UID))
			return nil
		}
	}

	w.log.Info("执行商城礼包领取", "pkg", mall.WechatPackageID, "day", now.Day())
	res, err := w.mall.Claim(ctx, w.sess.UID, w.sess.Token, mall.WechatPackageID)
	if err != nil {
		w.log.Warn("商城领取失败", "err", err)
		return nil
	}

	switch {
	case res.Status == 1:
		// 首次领取成功
		w.markMallClaimed(now, month, res.Info)
		w.log.Info("商城礼包领取成功", "pkg", mall.WechatPackageID, "info", res.Info)

	case mallAlreadyClaimed(res.Info):
		// 服务器明确说"已经领取过"：本月实际已完成（可能在游戏客户端领过，
		// 或上次运行领了但状态没持久化）。按"已完成"处理，本月不再重复请求。
		w.markMallClaimed(now, month, res.Info)
		w.log.Info("商城礼包本月已领取过，标记完成（不再重复领取）",
			"pkg", mall.WechatPackageID, "info", res.Info)

	case mallUnavailable(res.Info):
		// 未绑定微信等硬性不可领：本月内不再尝试，避免每天一次无效请求。
		if w.stateStore != nil {
				_ = w.stateStore.MallMarkUnavailable(w.sess.UID, month, res.Info)
			}
		w.log.Info("商城礼包本月不可领取，标记跳过",
			"pkg", mall.WechatPackageID, "info", res.Info)

	default:
		w.log.Warn("商城礼包领取未成功", "pkg", mall.WechatPackageID,
			"status", res.Status, "info", res.Info)
	}
	return nil
}

// markMallClaimed 记录本月已领取（内存 + 持久化）
func (w *Worker) markMallClaimed(now time.Time, month int, info string) {
	w.statsMu.Lock()
	w.mallOKKey = month
	w.statsMu.Unlock()
	if w.stateStore != nil {
		if err := w.stateStore.MallMarkClaimed(w.sess.UID, month, info); err != nil {
			w.log.Warn("商城礼包状态持久化失败", "err", err)
		}
	}
}

// mallAlreadyClaimed 判断服务器返回是否表示"本月已经领取过"。
//
// 实测（2026-09-01）：重复领取返回 status=0 + info="您已经领取过此礼包"。
// 用关键词匹配以兼容措辞微调，避免漏判导致每天重复请求 + UI 显示"未领取"。
func mallAlreadyClaimed(info string) bool {
	return strings.Contains(info, "已经领取") ||
		strings.Contains(info, "已领取") ||
		strings.Contains(info, "领取过")
}

// mallUnavailable 判断服务器返回是否表示"本月不可领取"（未绑定微信）。
//
// 实测样本（2026-09-01 生产日志，youjugua 未绑定微信）：
//
//	status=0, info="账号尚未绑定微信"
//
// 精确匹配该文案优先，再用关键词兜底（防止服务器措辞微调）。
// 注意必须与 mallAlreadyClaimed 互斥：已领取的提示不能被判成不可领取。
func mallUnavailable(info string) bool {
	if info == "" {
		return false
	}
	// 精确：生产实测文案
	if info == "账号尚未绑定微信" {
		return true
	}
	// 兜底：含"未绑定"/"尚未绑定"，或同时含"绑定"+"微信"
	return strings.Contains(info, "未绑定") ||
		strings.Contains(info, "尚未绑定") ||
		(strings.Contains(info, "绑定") && strings.Contains(info, "微信"))
}

// buildTasks 构建关键任务完成状态（供 UI 展示，固定顺序：
// 个人任务：每日签到 → 在线时长档位(2150-2155) → 连续签到奖励 → 月度礼包；
// 战队任务：固定 9 个日常任务（2000-2008，与客户端 guild_data_cn.xml 对应）→ 战队捐献。
// 注：个人在线任务（2150-2155）统计大厅在线时长、需手动领取（1174）；
// 战队任务统计游戏内在线/活跃时长（依赖建房/观战/加入挂机推进）、奖励自动发放（达成即完成）。
func (w *Worker) buildTasks() {
	var tasks []TaskStatus
	var teamTasks []TaskStatus
	now := time.Now()

	// 任务快照：w.tasks 由主循环（checkAndClaim）在 statsMu 下替换，
	// 这里必须在同一把锁下取快照后再遍历。直接 range w.tasks 会与写操作竞争，
	// 读到撕裂的 slice 头（ptr 与 len 不匹配）→ panic。
	w.statsMu.Lock()
	taskSnap := w.tasks
	w.statsMu.Unlock()

	// 战队状态（缓存，不触发网络查询）
	w.gangMu.Lock()
	gangOK := w.gangOK
	gangChecked := w.gangCheckedDay == dayKey(now)
	w.gangMu.Unlock()

	// 签到/商城标记：由主循环（maybeCheckIn / maybeMallClaim）与跨天重置在
	// statsMu 下写，本函数又可能被房间挂机 goroutine 调用，必须在同一把锁下
	// 取快照后再判断，直接读字段构成数据竞争。
	w.statsMu.Lock()
	checkInKey := w.checkInKey
	checkInOK := w.checkInOK
	mallOKKey := w.mallOKKey
	// 头像装扮（由 avatar.go 的 maybeDressAvatar 写入）
	avatarID := w.avatarGoodID
	avatarExp := w.avatarExp
	avatarLeft := w.avatarLeft
	avatarName := w.avatarName
	avatarMsg := w.avatarMsg
	w.statsMu.Unlock()

	// 每日签到
	switch {
	case !w.cfg.CheckInEnabled:
		tasks = append(tasks, TaskStatus{ID: 0, Name: "每日签到", State: TaskDisabled})
	case checkInKey == dayKey(now) && checkInOK:
		tasks = append(tasks, TaskStatus{ID: 0, Name: "每日签到", State: TaskDone})
	default:
		tasks = append(tasks, TaskStatus{ID: 0, Name: "每日签到", State: TaskPending})
	}

	// 在线时长档位：2150-2155（在线档位）作为个人任务。
	// 这些任务的奖励需要手动领取（checkAndClaim 提交 1174）；
	// 战队任务（2000-2008）按相同的时长档位独立跟踪（自动发放），在 teamTasks 中展示。
	onlineIDs := []uint64{2150, 2151, 2152, 2153, 2154, 2155}
	for _, id := range onlineIDs {
		for _, m := range taskSnap {
			if m.ID != id {
				continue
			}
			target := onlineMissionTarget(id)
			tasks = append(tasks, TaskStatus{
				ID:     m.ID,
				Name:   onlineMissionName(m.ID, target),
				State:  missionState(m),
				Detail: fmt.Sprintf("%s / %s", minutesText(m.Field2), minutesText(target)),
			})
			break
		}
	}

	// 连续签到奖励：2140/2141/2142（f2=当前已连续天数，目标 7/14/21 天）
	dayIDs := []uint64{2140, 2141, 2142}
	for _, id := range dayIDs {
		for _, m := range taskSnap {
			if m.ID != id {
				continue
			}
			target := dayTarget(id)
			tasks = append(tasks, TaskStatus{
				ID:    m.ID,
				Name:  fmt.Sprintf("连续签到 %d 天", target),
				State: missionState(m),
				Detail: fmt.Sprintf("%d/%d 天", m.Field2, target),
			})
			break
		}
	}

	// 商城礼包（每月 1-7 日微信绑定礼包；每月一次，领取后本月一直显示已完成）
	//
	// 判据优先取**持久化**状态（MallStore），因为"本月已领过"可能发生在上次运行
	// 或游戏客户端里，仅靠内存字段 mallOKKey 会在重启后误显示成"未领取"。
	month := monthKey(now)
	mallClaimed := mallOKKey == month
	mallUnavail := false
	if w.stateStore != nil {
		if w.stateStore.MallIsClaimed(w.sess.UID, month) {
			mallClaimed = true
		}
		if w.stateStore.MallIsUnavailable(w.sess.UID, month) {
			mallUnavail = true
		}
	}
	switch {
	case !w.cfg.MallEnabled:
		tasks = append(tasks, TaskStatus{ID: 100000, Name: "月度礼包", State: TaskDisabled})
	case now.Day() > 7:
		tasks = append(tasks, TaskStatus{ID: 100000, Name: "月度礼包", State: TaskNotDue})
	case mallClaimed:
		tasks = append(tasks, TaskStatus{ID: 100000, Name: "月度礼包", State: TaskDone})
	case mallUnavail:
		tasks = append(tasks, TaskStatus{ID: 100000, Name: "月度礼包", State: TaskDisabled,
			Detail: "不可领取（如未绑定微信）"})
	default:
		tasks = append(tasks, TaskStatus{ID: 100000, Name: "月度礼包", State: TaskPending})
	}

	// 经验头像（2026-09-09 调整）：佩戴头像不是"做任务"，不再放进任务徽章；
	// 状态随 UserStats.Avatar 推送，由前端渲染在「基础信息」列。
	var avatar AvatarInfo
	switch {
	case !w.cfg.AvatarEnabled:
		avatar = AvatarInfo{Note: "未启用"}
	case avatarID == 0:
		// 未佩戴：无可用加成头像，或佩戴失败（avatarMsg 记录具体原因）
		avatar = AvatarInfo{Note: avatarMsg}
	default:
		avatar = AvatarInfo{Worn: true, Name: avatarName, Exp: avatarExp, Left: avatarLeft}
	}

	// 战队任务：1172 响应同时返回个人任务（2150-2155 在线档位、2140-2142 连续签到）
	// 与大量历史/成就条目（2100 系列、1000 系列等，只有 ID 或有状态已完成）。
	// 客户端"战队任务"标签页仅展示固定 9 个日常任务（ID 2000-2008，
	// 与 guild_data_cn.xml 的 task 定义对应），这里按同一 ID 列表展示。
	//
	// allEmpty 是防御性标记：当 queryTeamInfo 的 f6.f4!=0 判定认为有战队，
	// 但服务器实际不跟踪这 9 个任务（全部 found==false）时，说明判定不可靠
	//（新注册/从未加入战队的账号也可能返回非零 f6.f4）。
	// 此时将 teamTasks 收敛为一条「未加入战队」提示，避免显示 9 个空条目。
	allEmpty := true
	switch {
	case !w.cfg.RoomHangEnabled:
		teamTasks = append(teamTasks, TaskStatus{ID: 200000, Name: "战盟挂机", State: TaskDisabled})
		allEmpty = false // 功能性禁用，不是"无战队"
	case gangChecked && !gangOK:
		teamTasks = append(teamTasks, TaskStatus{ID: 200000, Name: "战盟挂机", State: TaskDisabled, Detail: "未加入战队"})
		allEmpty = false
	case !gangChecked:
		teamTasks = append(teamTasks, TaskStatus{ID: 200000, Name: "战盟挂机", State: TaskPending, Detail: "检测战队状态中"})
		// allEmpty 保持 true：尚未检测，后续可能发现无战队
	default:
		// 已加入战队：展示固定 9 个战队日常任务（2000-2008）
		w.roomMu.Lock()
		roomOn, roomErr, since := w.roomOn, w.roomErr, w.roomSince
		w.roomMu.Unlock()
		if roomErr != nil && !errors.Is(roomErr, context.Canceled) {
			teamTasks = append(teamTasks, TaskStatus{ID: 200000, Name: "战盟挂机", State: TaskUnknown, Detail: roomErr.Error()})
		}
		hangDetailAdded := false
		for _, id := range teamMissionIDs {
			var mm Mission
			found := false
			for _, m := range taskSnap {
				if m.ID == id {
					mm = m
					found = true
					break
				}
			}
			if found {
				allEmpty = false
			}
			detail := teamMissionDetail(mm)
			if !found {
				detail = "未开始"
			} else if roomOn && mm.HasState && mm.Field3 != 1 && !hangDetailAdded {
				// 正在挂机时，在首个进行中的任务详情中附加挂机时长
				detail += fmt.Sprintf(" (挂机%d分)", int(time.Since(since).Minutes()))
				hangDetailAdded = true
			}
			teamTasks = append(teamTasks, TaskStatus{
				ID:     id,
				Name:   teamMissionName(id),
				State:  teamMissionState(mm, found),
				Detail: detail,
			})
		}
		// 防御性修正：如果 gangOK=true（queryTeamInfo 判定有战队）但 9 个任务的
		// 服务器数据全部缺失（found==false），说明该账号实际未加入任何战队，
		// queryTeamInfo 的 f6.f4!=0 判定对新注册/从未加入战队的账号不可靠。
		// 此时清掉已添加的 9 个空任务，替换为一条「未加入战队」提示。
		if allEmpty && len(teamMissionIDs) > 0 {
			teamTasks = teamTasks[:len(teamTasks)-len(teamMissionIDs)]
			teamTasks = append(teamTasks, TaskStatus{
				ID:     200001,
				Name:   "战队日常",
				State:  TaskDisabled,
				Detail: "未加入战队",
			})
		}
	}

	// 战队贡献捐献（战队任务，仅已加入战队的账号开启；每日目标贡献度，余额低于下限不捐）
	// contributeToday 由 maybeContribute（主循环）写、跨天重置写，这里加锁快照。
	// 当 allEmpty=true 时（防御性修正已触发），捐献也按「未加入战队」处理。
	w.statsMu.Lock()
	contributed := w.contributeToday
	w.statsMu.Unlock()
	switch {
	case !w.cfg.ContributeEnabled:
		teamTasks = append(teamTasks, TaskStatus{ID: 300000, Name: "战队捐献", State: TaskDisabled})
	case allEmpty || (gangChecked && !gangOK):
		teamTasks = append(teamTasks, TaskStatus{ID: 300000, Name: "战队捐献", State: TaskDisabled, Detail: "未加入战队"})
	case !gangChecked:
		teamTasks = append(teamTasks, TaskStatus{ID: 300000, Name: "战队捐献", State: TaskPending, Detail: "检测战队状态中"})
	case contributed >= w.cfg.ContributeDaily:
		teamTasks = append(teamTasks, TaskStatus{
			ID:    300000,
			Name:  "战队捐献",
			State: TaskDone,
			Detail: fmt.Sprintf("%d/%d 已捐", contributed, w.cfg.ContributeDaily),
		})
	default:
		teamTasks = append(teamTasks, TaskStatus{
			ID:    300000,
			Name:  "战队捐献",
			State: TaskDoing,
			Detail: fmt.Sprintf("已捐 %d/%d", w.contributeToday, w.cfg.ContributeDaily),
		})
	}

	// w.stats 同样被日志与主循环读取，加锁后整体替换两个切片
	w.statsMu.Lock()
	w.stats.Tasks = tasks
	w.stats.TeamTasks = teamTasks
	w.stats.Avatar = avatar
	w.statsMu.Unlock()
}

// missionState 由任务列表字段计算展示状态
func missionState(m Mission) string {
	switch {
	case !m.HasState:
		return TaskUnknown
	case m.Field3 == 1 && m.Field5 == 0:
		return TaskClaimable
	case m.Field3 == 1:
		return TaskDone
	default:
		return TaskDoing
	}
}

// teamMissionIDs 战队日常任务 ID（固定 9 个，与客户端 guild_data_cn.xml 的 task 定义一致）
var teamMissionIDs = []uint64{2000, 2001, 2002, 2003, 2004, 2005, 2006, 2007, 2008}

// teamMissionState 战队任务状态：奖励系统自动发放，达成(f3=1)即视为完成，
// 不显示"可领取"态（实测战队任务达成后自动发放，服务器不因客户端领取而设置 f5）。
// found 为 false 表示服务器本轮未返回该任务（视为未开始）。
func teamMissionState(m Mission, found bool) string {
	switch {
	case !found:
		return TaskPending
	case m.Field3 == 1:
		return TaskDone
	case m.HasState:
		return TaskDoing
	default:
		return TaskPending
	}
}

// isPersonalMission 判断任务 ID 是否属于个人任务（在线档位/连续签到）。
// 个人在线任务（2150-2155）统计大厅在线时长，需手动领取；战队任务（2000-2008）
// 是独立的一组任务，统计游戏内在线/活跃，奖励自动发放。
func isPersonalMission(id uint64) bool {
	switch id {
	case 2150, 2151, 2152, 2153, 2154, 2155, 2140, 2141, 2142:
		return true
	}
	return false
}

// teamMissionName 战队任务名称（ID→名称映射，与客户端 guild_data_cn.xml 的 title 一致）
func teamMissionName(id uint64) string {
	switch id {
	case 2000:
		return "在线2小时"
	case 2001:
		return "签到"
	case 2002:
		return "在任意游戏内玩或观战达到1小时"
	case 2003:
		return "在任意游戏内玩或观战达到2小时"
	case 2004:
		return "在任意游戏内玩或观战达到3小时"
	case 2005:
		return "使用小喇叭"
	case 2006:
		return "VIP免费领取活跃度"
	case 2007:
		return "SVIP免费领取活跃度"
	case 2008:
		return "SSVIP免费领取活跃度"
	}
	return fmt.Sprintf("战队任务 #%d", id)
}

// teamMissionTarget 战队任务目标值（2000-2004 以秒计，其余以次数计）
func teamMissionTarget(id uint64) uint64 {
	switch id {
	case 2000:
		return 7200 // 在线 2 小时
	case 2001:
		return 1 // 签到
	case 2002:
		return 3600 // 游戏 1 小时
	case 2003:
		return 7200 // 游戏 2 小时
	case 2004:
		return 10800 // 游戏 3 小时
	case 2005:
		return 1 // 使用小喇叭
	case 2006:
		return 1 // VIP 领取活跃度
	case 2007:
		return 1 // SVIP 领取活跃度
	case 2008:
		return 1 // SSVIP 领取活跃度
	}
	return 0
}

// teamMissionSeconds 判断任务进度是否以秒计（在线/游戏时长类任务）
func teamMissionSeconds(id uint64) bool {
	switch id {
	case 2000, 2002, 2003, 2004:
		return true
	}
	return false
}

// teamMissionDetail 战队任务进度详情
func teamMissionDetail(m Mission) string {
	target := teamMissionTarget(m.ID)
	switch {
	case m.Field3 == 1:
		if target > 0 {
			if teamMissionSeconds(m.ID) {
				return "已达成"
			}
			return fmt.Sprintf("已完成 %d/%d", m.Field2, target)
		}
		return "已达成"
	case m.Field2 > 0:
		if teamMissionSeconds(m.ID) && target > 0 {
			return fmt.Sprintf("%s / %s", minutesText(m.Field2), minutesText(target))
		}
		if target > 0 {
			return fmt.Sprintf("%d/%d", m.Field2, target)
		}
		return fmt.Sprintf("进度 %d", m.Field2)
	}
	return "未开始"
}

// onlineMissionTarget 在线时长任务 ID → 目标秒数（固定档位，与服务器实际一致）
func onlineMissionTarget(id uint64) uint64 {
	switch id {
	case 2000:
		return 7200 // 每日在线（通常 2 小时）
	case 2150:
		return 600 // 10 分钟
	case 2151:
		return 1800 // 30 分钟
	case 2152:
		return 3600 // 1 小时
	case 2153:
		return 7200 // 2 小时
	case 2154:
		return 10800 // 3 小时（挂机自动退出判定档；同时按 f2==10800 动态匹配）
	case 2155:
		return 14400 // 4 小时（预留）
	}
	return 0
}

// onlineMissionName 在线时长任务 ID + 目标秒数 → 显示名称
func onlineMissionName(id, seconds uint64) string {
	return "在线" + minutesText(seconds)
}

// minutesText 秒数 → "X分钟"/"X小时" 可读文本
func minutesText(seconds uint64) string {
	m := seconds / 60
	if m >= 60 && m%60 == 0 {
		return fmt.Sprintf("%d小时", m/60)
	}
	return fmt.Sprintf("%d分钟", m)
}

// dayTarget 连续签到任务 ID → 目标天数
func dayTarget(id uint64) uint64 {
	switch id {
	case 2140:
		return 7
	case 2141:
		return 14
	case 2142:
		return 21
	}
	return 0
}

// dayKey 返回日期 key（同年月日唯一）
func dayKey(t time.Time) int { return t.Year()*10000 + int(t.Month())*100 + t.Day() }

// monthKey 返回月份 key（同年月唯一）
func monthKey(t time.Time) int { return t.Year()*100 + int(t.Month()) }

// SubmitResult 是单任务提交结果
type SubmitResult struct {
	Success      bool
	AlreadyFinish bool
	Ret          uint64
	Msg          string
}

// submitOne 提交单个任务领取 MissionSubmitCMsg(1174)
func (w *Worker) submitOne(ctx context.Context, missionID uint64, day int32) (*SubmitResult, error) {
	body := protocol.BuildMissionSubmit(w.sess.UID, w.sess.Token, []uint64{missionID}, day)
	resp, err := w.sess.Gate.Game(ctx, w.sess.GateAddr, protocol.MissionSubmitCMsg, body)
	if err != nil {
		return nil, classify(err)
	}
	fields := proto.Parse(resp)
	ret := proto.GetVarint(fields, 1)
	msg := proto.GetString(fields, 2)
	res := &SubmitResult{Ret: ret, Msg: msg}
	if ret == 1 {
		res.Success = true
	} else if int64(ret) < 0 && (strings.Contains(msg, "ALREADY_FINISH") || strings.Contains(msg, "NOT_FINISH")) {
		res.AlreadyFinish = true
	} else {
		w.log.Debug("任务提交响应", "id", missionID, "ret", ret, "resp", hexBrief(resp))
	}
	return res, nil
}

// maybeCheckIn 每日签到（PlayerCheckInCMsg 1175, checkInType=2），每天只一次
func (w *Worker) maybeCheckIn(ctx context.Context) error {
	if !w.cfg.CheckInEnabled {
		return nil
	}
	now := time.Now()
	key := dayKey(now)
	if key == w.checkInKey {
		return nil
	}
	w.log.Info("执行每日签到", "day", now.Day())
	body := protocol.BuildCheckIn(w.sess.UID, w.sess.Token, 2)
	resp, err := w.sess.Gate.Game(ctx, w.sess.GateAddr, protocol.PlayerCheckInCMsg, body)
	if err != nil {
		return classify(err)
	}
	fields := proto.Parse(resp)
	ret := proto.GetVarint(fields, 1)
	msg := proto.GetString(fields, 2)
	w.log.Info("签到响应", "ret", ret, "msg", msg)
	// 无论成功与否当日不再重试（已签到/条件不满足短期内不会变化）
	// ret=1 签到成功；负数（实测 -23/-151 "point not meet"）表示当日已签，同样视为今日已完成
	w.statsMu.Lock()
	w.checkInKey = key
	w.checkInOK = ret == 1 || int64(ret) < 0
	checkOK := w.checkInOK
	w.statsMu.Unlock()
	if !checkOK {
		w.log.Warn("签到未成功", "ret", ret, "msg", msg)
	}
	return nil
}

// sleepCtx 可中断睡眠，返回 false 表示上下文已取消
func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

// classify 判断错误是否会话失效
func classify(err error) error {
	if err == nil {
		return nil
	}
	msg := strings.ToLower(err.Error())
	if strings.Contains(msg, "rejected") || strings.Contains(msg, "expired") ||
		strings.Contains(msg, "unauthorized") || strings.Contains(msg, "ret=") {
		return ErrSessionExpired
	}
	return err
}

// missionIDs 提取任务 ID 列表用于日志
func missionIDs(ms []Mission) []uint64 {
	ids := make([]uint64, 0, len(ms))
	for _, m := range ms {
		ids = append(ids, m.ID)
	}
	return ids
}

// hexBrief 响应摘要（前 64 字节）
func hexBrief(b []byte) string {
	const max = 64
	if len(b) <= max {
		return fmt.Sprintf("%x", b)
	}
	return fmt.Sprintf("%x...(%dB)", b[:max], len(b))
}
