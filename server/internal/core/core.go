// Package core 实现多账号动态管理：每账号独立 goroutine + 独立日志 + 断线重登
// 支持通过 Web UI 动态增删账号、启停单账号、查询运行状态
package core

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"math/rand"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"

	"youjuhang/internal/biz"
	"youjuhang/internal/config"
	"youjuhang/internal/gate"
	"youjuhang/internal/logx"
	"youjuhang/internal/session"
)

// 账号状态
const (
	StatusOffline     = "offline"      // 未运行
	StatusOnline      = "online"       // 在线挂机中
	StatusReloginWait = "relogin_wait" // 被踢等待重登
	StatusLoginFail   = "login_fail"   // 登录失败重试中
	StatusStopped     = "stopped"      // 已停止
	StatusDisabled    = "disabled"     // 因连续登录被拒（凭据问题）已自动禁用
)

// loginRejectLimit 连续被服务器拒绝登录多少次后自动禁用账号。
//
// 只统计"服务器明确拒绝"（gate.ErrLoginRejected），网络故障不计入。
// 取 5 次的原因：登录重试 backoff 为 5s→10s→20s→40s→80s，5 次累计约 2.5 分钟，
// 既能排除服务器临时维护/抖动（这类通常在几分钟内恢复），又能较快止损；
// 而密码错误是确定性失败，5 次足够确认，不会误伤。
const loginRejectLimit = 5

// AccountState 是账号运行时状态快照（UI 展示用）
type AccountState struct {
	Name             string  `json:"name"`
	Password         string  `json:"-"`
	MAC              string  `json:"mac"`
	Enabled          bool    `json:"enabled"`
	Running          bool    `json:"running"`
	Status           string  `json:"status"`
	Err              string  `json:"err,omitempty"`
	UID              uint32  `json:"uid"`
	Gate             string  `json:"gate"`
	Nick             string  `json:"nick"`
	Level            uint64  `json:"level"`
	Exp              uint64  `json:"exp"`
	ExpToday         uint64          `json:"exp_today"`
	Gold             uint64          `json:"gold"`
	// LevelGot 当前等级内**已获得**经验，由总经验推算（见 biz.UserStats.LevelGot）
	LevelGot uint64 `json:"level_got"`
	// LevelNeed 升到下一级所需**总**经验 = 10002-10001
	LevelNeed uint64 `json:"level_need"`
	LastActive       string          `json:"last_active,omitempty"`
	Tasks            []biz.TaskStatus `json:"tasks"`
	TeamTasks        []biz.TaskStatus `json:"team_tasks"`
	ExpireDate       string          `json:"expire_date,omitempty"` // 挂机到期日 YYYY-MM-DD，空=永久
	Expired          bool            `json:"expired"`               // 是否已过期（后端按日期计算）
}

// Manager 是账号动态管理器
type Manager struct {
	cfg     *config.Config
	cfgPath string
	gc      *gate.Client

	// stateStore 全局唯一的任务状态存储（2026-09-01 合并：当日捐献 + 月度礼包）。
	// 单一文件 task_state.json，单实例互斥锁保护并发写，
	// 每次写前自动备份为 .bak，损坏时可降级到 .bak 恢复。
	// 必须所有账号共享同一实例——并发时由 store 内部的 mu 串行化，
	// 避免分别 new 各自 load 旧状态、各自全量 write、后写覆盖先写。
	stateStore *biz.StateStore

	mu      sync.Mutex
	cancels map[string]*accountCancel
	wg      sync.WaitGroup
	states  map[string]*accountRuntime

	// rootCtx 是 Run 传入的根 context，所有账号 goroutine 都基于它派生。
	// 记录它是为了让账号 goroutine 内部（如被踢后重新登录）能安全地重启自己：
	// 若直接用"当前账号 ctx"派生新 ctx，重启会继承已被取消的旧 ctx 而立即退出。
	rootCtx context.Context
}

// accountCancel 是账号 goroutine 的取消句柄。
// 用指针而非裸 CancelFunc，便于区分"旧任务已取消、新任务已启动"的场景。
type accountCancel struct {
	cancel context.CancelFunc
	// done 在账号 goroutine 退出时关闭，供"继任者"等待前任真正退出。
	//
	// 为什么必须有（2026-09-01「启动后一直卡在登录中」事故）：
	// startLocked 过去只调用 old.cancel() 发取消信号就立刻启动新 goroutine，
	// 而旧 goroutine 可能仍卡在 HTTP 请求/enterLobby 里（最长可达数十秒）。
	// 于是新旧两个 goroutine 同时对同一账号登录；服务端"总是允许新登录并
	// 立即使旧会话失效"，两者互相顶下线，账号陷入 登录→被顶→重登 的死循环，
	// UI 上表现为「启动后一直卡在登录中」。
	done chan struct{}
}

// accountExitTimeout 等待旧账号 goroutine 退出的上限。
// 正常情况下旧 goroutine 在被取消后会立即退出（主循环 select 到 ctx.Done）；
// 极端情况（正卡在网关 HTTP 请求里）最多 30s。设上限是为了避免被卡死的
// 前任无限拖住继任者，超时仅告警并继续——宁可承担一次并发登录风险，
// 也好过账号永远起不来。
const accountExitTimeout = 30 * time.Second

// accountRuntime 是账号运行时状态（内部，带锁）
type accountRuntime struct {
	acc  config.Account // 值副本，避免与配置切片共享
	mu   sync.RWMutex
	st   AccountState
	sess *session.Session // 当前登录会话（供其他账号作"在线侦查"使用）

	// loginRejects 连续"被服务器明确拒绝"的次数（仅计 gate.ErrLoginRejected）。
	// 登录成功时清零；网络类失败不影响。达到 loginRejectLimit 即自动禁用账号。
	loginRejects int
}

// NewManager 创建管理器
func NewManager(cfgPath string, cfg *config.Config) *Manager {
	m := &Manager{
		cfg:     cfg,
		cfgPath: cfgPath,
		gc:      gate.New(cfg.LoginAddr),
		cancels: make(map[string]*accountCancel),
		states:  make(map[string]*accountRuntime),
	}
	// 全局唯一的捐献记录存储：所有账号共享，避免并发时记录相互覆盖
	m.stateStore = biz.NewStateStore(filepath.Join(filepath.Dir(cfgPath), biz.StateStoreFileName()))
	m.syncFromConfig()
	return m
}

// syncFromConfig 从配置同步账号列表（启动时调用）。
// 手工编辑 accounts.yaml 可能写出重名账号，这里按大小写不敏感检测并告警：
// 后者会覆盖前者（同名 key），若不提示，用户会困惑"少了一个账号"。
func (m *Manager) syncFromConfig() {
	seen := make(map[string]string, len(m.cfg.Accounts)) // 小写名 → 原始名
	for i := range m.cfg.Accounts {
		acc := m.cfg.Accounts[i]
		key := strings.ToLower(strings.TrimSpace(acc.Name))
		if prev, dup := seen[key]; dup {
			slog.Warn("配置文件存在重复账号名（忽略大小写），后者将覆盖前者，请检查 accounts.yaml",
				"name1", prev, "name2", acc.Name)
		}
		seen[key] = acc.Name

		rt := &accountRuntime{acc: acc}
		rt.st.Name = acc.Name
		rt.st.MAC = acc.MAC
		rt.st.Enabled = acc.Enabled == nil || *acc.Enabled
		rt.st.Status = StatusOffline
		// 配置里就是禁用的（多半是上次因连续登录被拒而自动禁用的）：
		// 直接标记 disabled，否则重启后只显示"离线"，用户看不出是凭据问题，
		// 也意识不到需要先改密码再启用。服务器给的原始 errStr 不写盘，
		// 这里只能给通用文案。
		if !rt.st.Enabled {
			rt.st.Status = StatusDisabled
			rt.st.Err = "账号已禁用（此前登录被服务器拒绝）"
		}
		rt.st.ExpireDate = acc.ExpireDate
		m.states[acc.Name] = rt
	}
}

// expireCheckInterval 到期检查间隔。
// 到期判定粒度是"天"，只需在跨天后的第一个检查点生效即可，无需频繁轮询。
const expireCheckInterval = time.Hour

// Run 启动所有启用账号并阻塞，直到 ctx 取消后统一停止
func (m *Manager) Run(ctx context.Context) {
	m.rootCtx = ctx
	m.StartAll(ctx)
	go m.expireWatcher(ctx)
	go m.metricsLogger(ctx)
	<-ctx.Done()
	m.StopAll()
	m.wg.Wait()
}

// metricsInterval 运行时指标输出间隔。
const metricsInterval = 30 * time.Minute

// metricsLogger 周期性输出进程级运行时指标（goroutine 数、堆内存、GC 次数）。
//
// 为什么需要（2026-09-01「进程无声消失」排查）：
// 若崩溃源于**资源泄漏或 OOM**，进程死亡前 goroutine 数与堆内存会呈**单调增长**。
// 每 30 分钟记一条，就能在崩溃后回看曲线判断：
//   - goroutine 数持续爬升 → goroutine 泄漏（某个循环每次都起新 goroutine 且旧的不退出）
//   - HeapAlloc 持续爬升且不回落 → 内存泄漏或 GC 压力，最终 OOM 被系统终结
//   - 两者都平稳 → 排除泄漏，应转向 fatal error（并发 map 等）方向
func (m *Manager) metricsLogger(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 16384)
			n := runtime.Stack(buf, false)
			slog.Error("指标 goroutine panic 已捕获，进程继续运行",
				"panic", r, "stack", string(buf[:n]))
		}
	}()
	t := time.NewTicker(metricsInterval)
	defer t.Stop()
	// 启动即记一条，作为基线
	logMetrics()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			logMetrics()
		}
	}
}

// logMetrics 输出一次运行时指标
func logMetrics() {
	var ms runtime.MemStats
	runtime.ReadMemStats(&ms)
	slog.Info("运行时指标",
		"goroutines", runtime.NumGoroutine(),
		"heap_alloc_mb", ms.HeapAlloc>>20,
		"heap_sys_mb", ms.HeapSys>>20,
		"sys_mb", ms.Sys>>20,
		"num_gc", ms.NumGC,
		"uptime_min", int(time.Since(startedAt).Minutes()))
}

// startedAt 进程启动时刻（供指标计算运行时长）
var startedAt = time.Now()

// expireWatcher 定期检查挂机到期的账号并自动停止（对应"超过到期日后自动退出"）。
// 子 goroutine，必须自带 recover：-H windowsgui 下未捕获的 panic 会让进程无声消失。
func (m *Manager) expireWatcher(ctx context.Context) {
	defer func() {
		if r := recover(); r != nil {
			buf := make([]byte, 16384)
			n := runtime.Stack(buf, false)
			slog.Error("expireWatcher panic 已捕获，进程继续运行",
				"panic", r, "stack", string(buf[:n]))
		}
	}()
	t := time.NewTicker(expireCheckInterval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.stopExpiredAccounts()
		}
	}
}

// stopExpiredAccounts 停止所有已到期且仍在运行的账号。
// 先收集名单再释放锁后停止：StopAccount 内部要获取 m.mu，持锁调用会死锁。
func (m *Manager) stopExpiredAccounts() {
	m.mu.Lock()
	var expired []string
	now := time.Now()
	for name, rt := range m.states {
		if _, running := m.cancels[name]; !running {
			continue
		}
		rt.mu.RLock()
		expire := rt.acc.ExpireDate
		rt.mu.RUnlock()
		if config.IsExpired(expire, now) {
			expired = append(expired, name)
		}
	}
	m.mu.Unlock()

	for _, name := range expired {
		slog.Info("账号挂机已到期，自动停止", "account", name)
		m.StopAccount(name)
	}
}

// ErrAccountExpired 账号挂机已过期（Web 层据此提示用户确认是否仍要启动）
var ErrAccountExpired = errors.New("挂机已过期")

// StartAll 启动所有"启用且未过期"的账号，返回因过期而跳过的账号数。
// 已过期的账号不会被自动启动，用户可在 UI 上确认后单独强制启动。
func (m *Manager) StartAll(ctx context.Context) int {
	m.mu.Lock()
	defer m.mu.Unlock()
	now := time.Now()
	skipped := 0
	for name, rt := range m.states {
		if !rt.st.Enabled {
			continue
		}
		rt.mu.RLock()
		expire := rt.acc.ExpireDate
		rt.mu.RUnlock()
		if config.IsExpired(expire, now) {
			slog.Info("账号已过期，全部启动中跳过", "account", name, "expire_date", expire)
			skipped++
			continue
		}
		m.startLocked(ctx, name)
	}
	return skipped
}

// findByNameLocked 按账号名查找（**大小写不敏感**），返回实际配置的账号名。
// 用于重名校验：让错误提示回显用户已存在的那个名字，而不是新输入的大小写变体。
// 调用方需持有 m.mu。
func (m *Manager) findByNameLocked(name string) (string, bool) {
	for n := range m.states {
		if strings.EqualFold(n, name) {
			return n, true
		}
	}
	return "", false
}

// StartAccount 启动单个账号；已过期时返回 ErrAccountExpired 且不启动。
func (m *Manager) StartAccount(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.states[name]
	if !ok {
		return fmt.Errorf("账号 %q 不存在", name)
	}
	rt.mu.RLock()
	expire := rt.acc.ExpireDate
	rt.mu.RUnlock()
	if config.IsExpired(expire, time.Now()) {
		return fmt.Errorf("%w（到期日 %s）", ErrAccountExpired, expire)
	}
	m.startLocked(ctx, name)
	return nil
}

// StartAccountForce 强制启动单个账号，忽略过期检查。
// 仅供用户在 UI 上确认「账号已过期，仍要启动」后调用。
func (m *Manager) StartAccountForce(ctx context.Context, name string) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.states[name]; !ok {
		return fmt.Errorf("账号 %q 不存在", name)
	}
	m.startLocked(ctx, name)
	return nil
}

// startLocked 启动账号 goroutine（需持有 m.mu）。
// 若账号已在运行（如被踢后等待恢复），则先取消旧任务再重启，实现「恢复挂机」。
//
// 并发登录防护（2026-09-01 事故修复）：
// old.cancel() 只是发出取消信号，旧 goroutine 未必已经退出（可能仍卡在网关
// HTTP 请求或 enterLobby 重试里）。若此时新 goroutine 立即开始登录，就会出现
// 两个 goroutine 同时对同一账号登录、互相顶下线的死循环。
// 因此「等待前任退出」由**新 goroutine 自己**在启动后、登录前完成
// （不能在 startLocked 里等：此时持有 m.mu，而旧 goroutine 退出路径最后
// 也要拿 m.mu 做清理，持锁等待会死锁）。
func (m *Manager) startLocked(ctx context.Context, name string) {
	var old *accountCancel
	if prev, running := m.cancels[name]; running {
		old = prev
		old.cancel()
		delete(m.cancels, name)
	}
	base := ctx
	if m.rootCtx != nil {
		base = m.rootCtx
	}
	ctx2, cancel := context.WithCancel(base)
	ac := &accountCancel{cancel: cancel, done: make(chan struct{})}
	m.cancels[name] = ac
	rt := m.states[name]
	// 每次启动都是新一轮尝试：清空"连续被拒"计数。
	// 否则用户改好密码重新启用后，残留的旧计数会让它更快再次被禁用。
	if rt != nil {
		rt.mu.Lock()
		rt.loginRejects = 0
		rt.mu.Unlock()
	}
	m.wg.Add(1)
	go func() {
		defer m.wg.Done()
		// done 必须最先注册（defer 后进先出），保证继任者一定能等到信号
		defer close(ac.done)
		// 账号 goroutine 的 panic 兜底：不捕获的话，任一账号的 panic 会终结
		// 整个进程（main 的 recover 管不到子 goroutine），且 -H windowsgui 下
		// stderr 被丢弃、日志无痕，外部只看到进程消失。见 biz.recoverGoroutine。
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 16384)
				n := runtime.Stack(buf, false)
				slog.Error("账号 goroutine panic 已捕获，进程继续运行",
					"account", name, "panic", r, "stack", string(buf[:n]))
			}
		}()

		// 等前任真正退出后再登录，避免并发登录互相顶下线
		m.waitAccountExit(old, name)
		// 等待期间用户可能又点了停止：直接退出，不做任何状态写入
		if ctx2.Err() != nil {
			return
		}

		m.runAccount(ctx2, name, rt)

		// 只有自己仍是"现任"时才清理状态。
		// 若继任者已经顶上（用户快速停止→启动），这里再 setRunning(false)
		// 会把继任者刚设置的"运行中/登录中"覆盖成"已停止"，导致 UI 状态错乱。
		m.mu.Lock()
		current := m.cancels[name] == ac
		if current {
			delete(m.cancels, name)
		}
		m.mu.Unlock()
		if current {
			// 因连续登录被拒而自动禁用的账号，必须保留 disabled 状态与错误原因，
			// 不能被通用的"已停止"覆盖——否则用户看不到"密码错误"提示，
			// 只会看到一个普通离线账号，无从判断该改密码还是重新启用。
			rt.mu.RLock()
			st, errMsg := rt.st.Status, rt.st.Err
			rt.mu.RUnlock()
			if st == StatusDisabled {
				rt.setRunning(false, StatusDisabled, errMsg)
			} else {
				rt.setRunning(false, StatusStopped, "")
			}
		}
	}()
}

// waitAccountExit 等待旧的账号 goroutine 退出（有上限，避免被卡死的前任拖住）。
// old 为 nil（无前任）时立即返回。
func (m *Manager) waitAccountExit(old *accountCancel, name string) {
	if old == nil || old.done == nil {
		return
	}
	t := time.NewTimer(accountExitTimeout)
	defer t.Stop()
	select {
	case <-old.done:
		return
	case <-t.C:
		slog.Warn("等待旧账号任务退出超时，继续启动（可能出现一次并发登录）",
			"account", name, "timeout", accountExitTimeout)
	}
}

// StopAccount 停止单个账号（异步，等待其退出）
func (m *Manager) StopAccount(name string) {
	m.mu.Lock()
	ac, ok := m.cancels[name]
	m.mu.Unlock()
	if ok {
		ac.cancel()
	}
}

// StopAll 停止所有账号
func (m *Manager) StopAll() {
	m.mu.Lock()
	for _, ac := range m.cancels {
		ac.cancel()
	}
	m.mu.Unlock()
}

// AddAccount 新增账号并保存配置。
// days > 0 时按"从今天起 days 天"设置到期日（例：今天 8/31 填 3 → 到期日 9/3，
// 8/31~9/3 均可挂机，9/4 起过期）；days <= 0 表示永久有效。
//
// 重名检查**大小写不敏感**：只差大小写（如 abc / ABC）也视为同一账号并拒绝。
// 理由：若放行，两个账号在服务端大概率被识别为同一用户，登录后互相顶下线，
// 表现为两个账号反复掉线重连。宁可拒绝，也不能让它们同时挂机。
func (m *Manager) AddAccount(acc config.Account, days int) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	if exist, ok := m.findByNameLocked(acc.Name); ok {
		return fmt.Errorf("账号 %q 已存在，不能重复添加", exist)
	}
	if acc.Enabled == nil {
		en := true
		acc.Enabled = &en
	}
	acc.ExpireDate = config.CalcExpireDate(time.Now(), days)
	m.cfg.Accounts = append(m.cfg.Accounts, acc)
	rt := &accountRuntime{acc: acc}
	rt.st.Name = acc.Name
	rt.st.MAC = acc.MAC
	rt.st.Enabled = true
	rt.st.Status = StatusOffline
	rt.st.ExpireDate = acc.ExpireDate
	m.states[acc.Name] = rt
	if days > 0 {
		slog.Info("新增账号并设定挂机到期日", "account", acc.Name,
			"days", days, "expire_date", acc.ExpireDate)
	}
	return m.saveLocked()
}

// UpdateAccount 更新账号配置。
//
// 运行中的账号**可以直接修改**，不再要求先停止。按字段分两类处理：
//
//   - 到期日（add_days / expire_date）：热更新，立即生效。expireWatcher 读的就是
//     rt.acc.ExpireDate，因此改完即被下一次巡检看到；但巡检间隔是 1 小时，
//     所以这里额外做一次即时判定——若改完已过期，立刻停止挂机，不等巡检。
//
//   - 登录凭据（password / mac）：配置与磁盘立即更新，但**当前会话不生效**。
//     runAccount 启动时有 acc := rt.acc 的值拷贝，运行中会话用的仍是旧凭据，
//     新凭据要等下次启动。此时返回 pendingRestart=true，由调用方提示用户。
//
// addDays 是在**原到期日**基础上的增减量（不是从今天重算）：
//
//	addDays > 0 → 延长；addDays < 0 → 缩短；addDays == 0 → 到期日保持不变。
//
// 原账号为永久（无到期日）时：正数从今天起算（设定期限），负数无基数可减少则保持永久。
func (m *Manager) UpdateAccount(name string, acc config.Account, addDays int) (pendingRestart bool, err error) {
	m.mu.Lock()
	rt, ok := m.states[name]
	if !ok {
		m.mu.Unlock()
		return false, fmt.Errorf("账号 %q 不存在", name)
	}
	_, running := m.cancels[name]
	acc.Enabled = boolPtr(rt.acc.Enabled == nil || *rt.acc.Enabled) // 保留启用状态

	// 前端编辑表单只填"修改的字段"，未填字段解码为零值。
	// 若直接用 acc 替换整条记录，会把未提供的 Name/Password/MAC 清空并写盘，
	// 下次启动因缺失密码报"missing password"（2026-09-01 事故）。
	// 约定：空字符串 = "不修改"，沿用 rt.acc 中现存值；
	// 用户真想清除密码则需要走"删除后重添"路径。
	if acc.Name == "" {
		acc.Name = rt.acc.Name
	}
	if acc.Password == "" {
		acc.Password = rt.acc.Password
	}
	if acc.MAC == "" {
		acc.MAC = rt.acc.MAC
	}

	// 到期日增减基数：优先用请求体显式带的 expire_date，否则取配置中现有的值。
	// 具体以「原到期日」还是「今天」为基数，由 config.ExtendExpireDate 按账号当前
	// 是否已过期决定：已过期时正数/0 以今天为基数（否则续期后仍是过去日期、
	// 账号依旧不可用），其余情况一律以原到期日为基数。
	base := acc.ExpireDate
	if base == "" {
		base = rt.acc.ExpireDate
	}
	oldExpire := base
	acc.ExpireDate = config.ExtendExpireDate(base, time.Now(), addDays)

	// 运行中改了凭据类字段：配置立即落盘，但当前会话（runAccount 的值拷贝）
	// 仍用旧值，新凭据下次启动才生效——需要提示用户。
	rt.mu.RLock()
	credsChanged := running &&
		(acc.Password != rt.acc.Password || acc.MAC != rt.acc.MAC)
	rt.mu.RUnlock()

	for i := range m.cfg.Accounts {
		if m.cfg.Accounts[i].Name == name {
			m.cfg.Accounts[i] = acc
			break
		}
	}
	rt.mu.Lock()
	rt.acc = acc
	rt.st.Name = acc.Name
	rt.st.MAC = acc.MAC
	rt.st.Enabled = *acc.Enabled
	rt.st.ExpireDate = acc.ExpireDate
	rt.mu.Unlock()
	if addDays != 0 {
		slog.Info("账号挂机到期日已调整", "account", name,
			"delta_days", addDays, "from", oldExpire, "to", acc.ExpireDate)
	}
	if err := m.saveLocked(); err != nil {
		m.mu.Unlock()
		return false, err
	}

	// 到期日改完立即判定：已过期就停掉，不等 expireWatcher（间隔 1 小时）。
	// StopAccount 内部要获取 m.mu，必须先解锁再调，否则死锁。
	stopNow := running && config.IsExpired(acc.ExpireDate, time.Now())
	m.mu.Unlock()

	if stopNow {
		slog.Info("账号到期日已改为已过期，自动停止挂机",
			"account", name, "expire_date", acc.ExpireDate)
		m.StopAccount(name)
	}
	return credsChanged, nil
}

// SetEnabled 启用/禁用账号（禁用=停止，启用=不自动启动）
func (m *Manager) SetEnabled(name string, enabled bool) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt, ok := m.states[name]
	if !ok {
		return fmt.Errorf("账号 %q 不存在", name)
	}
	for i := range m.cfg.Accounts {
		if m.cfg.Accounts[i].Name == name {
			m.cfg.Accounts[i].Enabled = boolPtr(enabled)
			break
		}
	}
	rt.acc.Enabled = boolPtr(enabled)
	rt.st.Enabled = enabled
	// 用户手动重新启用（通常是改对了密码）：清掉"自动禁用"的痕迹。
	// 否则状态一直卡在 disabled，用户会以为启用没生效；
	// 计数也必须清零，否则残留计数会让它更快被再次禁用。
	if enabled {
		rt.mu.Lock()
		if rt.st.Status == StatusDisabled {
			rt.st.Status = StatusOffline
			rt.st.Err = ""
		}
		rt.loginRejects = 0
		rt.mu.Unlock()
	}
	return m.saveLocked()
}

// RemoveAccount 删除账号（先停止）
func (m *Manager) RemoveAccount(name string) error {
	m.mu.Lock()
	if ac, ok := m.cancels[name]; ok {
		ac.cancel() // 停止
	}
	delete(m.states, name)
	for i := range m.cfg.Accounts {
		if m.cfg.Accounts[i].Name == name {
			m.cfg.Accounts = append(m.cfg.Accounts[:i], m.cfg.Accounts[i+1:]...)
			break
		}
	}
	m.mu.Unlock()
	return m.saveLocked()
}

// UpdateGlobal 更新全局配置并保存（运行中账号下轮生效）
func (m *Manager) UpdateGlobal(fn func(c *config.Config)) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	fn(m.cfg)
	return m.saveLocked()
}

// GlobalConfig 返回全局配置副本
func (m *Manager) GlobalConfig() config.Config {
	m.mu.Lock()
	defer m.mu.Unlock()
	return *m.cfg
}

// Snapshot 返回所有账号状态快照（按配置顺序）
func (m *Manager) Snapshot() []AccountState {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]AccountState, 0, len(m.cfg.Accounts))
	for i := range m.cfg.Accounts {
		name := m.cfg.Accounts[i].Name
		if rt, ok := m.states[name]; ok {
			out = append(out, rt.snapshot())
		}
	}
	return out
}

// LogPath 返回账号日志文件路径
func (m *Manager) LogPath(name string) (string, bool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if _, ok := m.states[name]; !ok {
		return "", false
	}
	dir := m.cfg.LogDir
	if dir == "" {
		dir = "logs"
	}
	return dir + "/" + name + ".log", true
}

func (m *Manager) saveLocked() error {
	if err := m.cfg.Save(m.cfgPath); err != nil {
		slog.Error("配置保存失败", "err", err)
		return err
	}
	return nil
}

// snapshot 返回状态副本。
// 到期日与过期标记每次从 acc 实时计算（不缓存），
// 保证跨天时无需重启也能立即反映"今日是否已过期"。
func (rt *accountRuntime) snapshot() AccountState {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	st := rt.st
	st.ExpireDate = rt.acc.ExpireDate
	st.Expired = config.IsExpired(rt.acc.ExpireDate, time.Now())
	return st
}

func (rt *accountRuntime) setRunning(running bool, status, err string) {
	rt.mu.Lock()
	rt.st.Running = running
	if status != "" {
		rt.st.Status = status
	}
	rt.st.Err = err
	rt.mu.Unlock()
}

func (rt *accountRuntime) setStatus(status, err string) {
	rt.mu.Lock()
	rt.st.Status = status
	rt.st.Err = err
	rt.mu.Unlock()
}

func (rt *accountRuntime) updateStats(s biz.UserStats) {
	rt.mu.Lock()
	rt.st.Nick = s.Nick
	rt.st.Level = s.Level
	rt.st.Exp = s.Exp
	rt.st.ExpToday = s.ExpToday
	rt.st.Gold = s.Gold
	rt.st.LevelGot = s.LevelGot()
	rt.st.LevelNeed = s.LevelTotal()
	rt.st.Tasks = append([]biz.TaskStatus(nil), s.Tasks...)
	rt.st.TeamTasks = append([]biz.TaskStatus(nil), s.TeamTasks...)
	rt.st.LastActive = time.Now().Format("2006-01-02 15:04:05")
	rt.mu.Unlock()
}

func boolPtr(b bool) *bool { return &b }

// ensureMAC 为账号补齐自动生成的 MAC（登录协议需要设备标识，用户无需配置）
func (m *Manager) ensureMAC(name string, rt *accountRuntime) {
	m.mu.Lock()
	defer m.mu.Unlock()
	rt.mu.Lock()
	defer rt.mu.Unlock()
	if rt.acc.MAC != "" {
		return
	}
	mac := genMAC()
	rt.acc.MAC = mac
	rt.st.MAC = mac
	for i := range m.cfg.Accounts {
		if m.cfg.Accounts[i].Name == name {
			m.cfg.Accounts[i].MAC = mac
			break
		}
	}
	_ = m.saveLocked()
}

// genMAC 生成随机 MAC 地址（本地管理单播，格式与游聚客户端一致）
func genMAC() string {
	b := make([]byte, 6)
	for i := range b {
		b[i] = byte(rand.Intn(256))
	}
	b[0] = b[0]&0xFE | 0x02 // 本地管理单播地址，避免误识别为真实网卡
	return fmt.Sprintf("%02X-%02X-%02X-%02X-%02X-%02X", b[0], b[1], b[2], b[3], b[4], b[5])
}

// noteLoginRejected 记录一次"服务器明确拒绝登录"，达到阈值则自动禁用该账号。
//
// 返回 true 表示账号已被禁用，调用方（runAccount）应立即退出运行循环。
//
// 为什么要自动禁用：密码错误/账号不存在是**确定性失败**，重试一万次结果相同。
// 放任其按 5s→10min 的 backoff 无限重试，既浪费资源，又容易被服务端风控判定为
// 撞库/暴力破解，进而影响同 IP 下其他正常账号。
//
// 调用方是从账号 goroutine 进入的，**不持有 m.mu**，因此这里可以安全调用
// SetEnabled / StopAccount（二者内部各自获取锁；StopAccount 只是发取消信号，
// 真正的退出由 runAccount 返回后 startLocked 的清理分支完成）。
func (m *Manager) noteLoginRejected(ctx context.Context, name string, rt *accountRuntime, err error, lg *slog.Logger) bool {
	rt.mu.Lock()
	rt.loginRejects++
	n := rt.loginRejects
	rt.mu.Unlock()

	if n < loginRejectLimit {
		lg.Warn("登录被服务器拒绝", "count", n, "limit", loginRejectLimit, "err", err)
		return false
	}

	lg.Error("登录连续被服务器拒绝，自动禁用账号",
		"count", n, "limit", loginRejectLimit, "err", err)
	slog.Error("账号连续登录被拒（疑似密码错误/账号不存在），已自动禁用，修改密码后请手动启用",
		"account", name, "count", n, "err", err)

	// 先置状态再落盘：SetEnabled 只改配置不碰状态，两者顺序不影响正确性，
	// 但先设状态可避免 UI 在禁用瞬间显示"已停止"再跳成"已禁用"的闪烁。
	rt.setStatus(StatusDisabled, err.Error())
	if e := m.SetEnabled(name, false); e != nil {
		slog.Error("自动禁用账号失败（配置未落盘）", "account", name, "err", e)
		return false
	}
	// SetEnabled 不会停止正在运行的任务，这里显式取消，让 runAccount 退出。
	m.StopAccount(name)
	return true
}

// runAccount 单账号运行循环：登录 → 业务 → 失效重登
func (m *Manager) runAccount(ctx context.Context, name string, rt *accountRuntime) {
	m.ensureMAC(name, rt)
	acc := rt.acc
	cfg := m.cfg
	lg, err := logx.New(acc.Name, logx.Options{
		Dir:     cfg.LogDir,
		Console: cfg.LogConsole,
	})
	if err != nil {
		slog.Error("日志初始化失败", "account", acc.Name, "err", err)
		return
	}
	lg.Info("账号任务启动")
	rt.setRunning(true, StatusOffline, "")

	backoff := 5 * time.Second
	for {
		if ctx.Err() != nil {
			return
		}
		rt.setStatus(StatusLoginFail, "")
		sess, err := session.Login(ctx, m.gc, &acc)
		if err != nil {
			if ctx.Err() != nil {
				return
			}
			// 服务器明确拒绝（密码错误/账号不存在/封禁等）：连续多次后自动禁用。
			// 网络类失败不在此列——那类是可恢复的，应继续重试。
			if errors.Is(err, gate.ErrLoginRejected) {
				if m.noteLoginRejected(ctx, name, rt, err, lg) {
					return // 已达阈值：账号已禁用并落盘，退出运行循环
				}
			}
			rt.setStatus(StatusLoginFail, err.Error())
			lg.Error("登录失败", "err", err, "retry_in", backoff.Round(time.Second))
			if !sleepCtx(ctx, backoff) {
				return
			}
			backoff = minDur(backoff*2, 10*time.Minute)
			continue
		}
		// 登录成功后必须先确认本任务未被取消：
		// 若期间用户点了停止（或又启动了新任务），这次登录会**顶掉继任者的会话**
		// （服务端允许新登录、立即使旧会话失效），继任者随即检测到会话失效并重登，
		// 两边互相踢，账号表现为一直卡在"登录中"。已取消就直接退出，不做任何写入。
		if ctx.Err() != nil {
			return
		}
		backoff = 5 * time.Second
		// 登录成功：凭据没问题，清空连续被拒计数（例如之前只是服务器临时抽风）
		rt.mu.Lock()
		rt.loginRejects = 0
		rt.mu.Unlock()
		rt.setStatus(StatusOnline, "")
		rt.updateUID(sess)
		lg.Info("登录成功", "uid", sess.UID, "gate", sess.GateAddr)

		// 崩溃兜底：本账号协程 panic 时尽力发 534 清游聚 session，避免僵尸
		defer func() {
			if r := recover(); r != nil {
				buf := make([]byte, 16384)
				_ = runtime.Stack(buf, false)
				lg.Error("账号协程panic，兜底登出", "panic", r)
				if sess != nil {
					func() {
						defer func() { _ = recover() }()
						sess.Release()
					}()
				}
			}
		}()

		// 当日捐献记录：使用全局共享的 store（按 uid 区分，避免多账号并发记录覆盖）
		w := biz.NewWorker(sess, cfg, lg, m.stateStore)
		w.SetStatsObserver(func(s biz.UserStats) {
			rt.updateStats(s)
			// 仅在账号未被停止时更新状态为"在线"；
			// 停止后（ctx 已取消）不再覆盖 StatusStopped / StatusReloginWait 等状态，
			// 避免 Worker 退出路径上的 notifyStats 把"已停止"刷回"在线"。
			if ctx.Err() == nil {
				rt.setStatus(StatusOnline, "")
			}
		})
		err = w.Run(ctx)
		if ctx.Err() != nil {
			// 账号被主动停止（用户点停止 / 进程优雅退出 / StopAll）：主动登出(534)释放会话，
			// 否则游聚侧残留僵尸 session，被本账号顶掉的对端会永远卡在"等待恢复"。
			sess.Release()
			return
		}
		// 会话失效：通常是被其他设备（用户客户端）登录踢下线。
		// 实测确认：登录服务器总是允许新登录并使旧会话立即失效，登录响应无任何
		// "账号已在线"信息；社交协议(1176 action=1)查询在线状态对**好友与非好友都准确**
		// （2026-09-01 tools/probe_social.py 真机验证：非好友也能查到 isOnline）。
		// 因此优先用其他在线账号只读侦查本账号在线状态：在线则不重登（避免抢占用户），
		// 离线则自动重登；无可用侦查账号时回退到定时/手动等待。
		rt.setSession(nil)
		if !m.waitAfterKick(ctx, lg, rt, name, err) {
			return
		}
	}
}

// waitAfterKick 被顶下线后的等待策略，返回是否继续主循环重登。
// 1. 有其他在线账号 → 在线侦查：目标账号离线后自动重登恢复挂机
// 2. 否则           → 兜底探测：每 fallbackInterval 随机挑一个账号试登，
//                      登录成功即说明服务器可用，再用它侦查目标在线状态
//                      （用于服务器维护/整机断网后的自动恢复）
func (m *Manager) waitAfterKick(ctx context.Context, lg *slog.Logger, rt *accountRuntime, name string, err error) bool {
	if probe, probeName := m.pickProbeSession(name, nil); probe != nil {
		lg.Info("使用在线账号侦查目标在线状态", "account", name, "probe", probeName)
		if ok := m.probeWaitRelogin(ctx, lg, rt, name, probe, probeName); ok {
			return ctx.Err() == nil
		}
		if ctx.Err() != nil {
			return false
		}
		lg.Info("侦查账号不可用，回退到兜底探测", "account", name)
	}
	return m.fallbackWaitRelogin(ctx, lg, rt, name, err)
}

// probeInterval 在线状态侦查间隔
const probeInterval = 60 * time.Second

// probeWaitRelogin 用侦查账号周期性查询目标账号在线状态：
// 离线（f100=0，权威）则返回 true 触发自动重登恢复挂机；
// 在线（f100=1，用户在游戏客户端）则继续等待，绝不抢占——
// 不引入任何"时间保底强制恢复"，用户玩多久都不该被顶（2026-09-01 修正）。
// 查询失败（侦查账号会话失效等）时随机换其他在线账号；无可用侦查账号则返回 false 交兜底。
func (m *Manager) probeWaitRelogin(ctx context.Context, lg *slog.Logger, rt *accountRuntime, name string, probe *session.Session, probeName string) bool {
	targetUID := rt.snapshot().UID
	lastCheck := ""
	probeAcc := probe
	accName := probeName
	failed := make(map[string]bool) // 本轮已失效、不再选中的侦查账号
	for {
		msg := "检测到账号在线（可能您在游戏中），挂机暂停，离线后自动恢复挂机"
		if lastCheck != "" {
			msg += "；最近检查 " + lastCheck
		}
		rt.setStatus(StatusReloginWait, msg)
		res, err := biz.QueryUserOnline(ctx, probeAcc, targetUID)
		if err != nil {
			// 侦查失败（侦查账号会话失效等）：随机换一个**尚未失败过**的在线账号，
			// 并等待 probeInterval 再重试，避免失效时每秒疯狂重试造成流量浪费与封号风险。
			lg.Warn("在线状态查询失败，随机换用其他侦查账号",
				"account", name, "probe", accName, "err", err)
			failed[accName] = true
			next, nextName := m.pickProbeSession(name, failed)
			if next == nil {
				// 已无其他可用侦查账号：清空 failed 重新计一轮（账号可能已恢复在线），
				// 仍无则继续等待（下一轮仍可能超时触发保底）。
				next, nextName = m.pickProbeSession(name, nil)
				if next == nil {
					lg.Info("无可用侦查账号，继续等待", "account", name)
					lastCheck = time.Now().Format("15:04:05")
					select {
					case <-time.After(probeInterval):
					case <-ctx.Done():
						return false
					}
					continue
				}
				for k := range failed {
					delete(failed, k)
				}
			}
			probeAcc, accName = next, nextName
			lg.Info("已切换侦查账号", "account", name, "probe", accName)
			lastCheck = time.Now().Format("15:04:05")
			select {
			case <-time.After(probeInterval):
			case <-ctx.Done():
				return false
			}
			continue
		}
		lastCheck = time.Now().Format("15:04:05")
		if !res.Online {
			// 权威判定为离线（f100=0）：用户在游戏客户端已退出，恢复挂机。
			// 该结果对好友/非好友都准确（1176 action=1 真机验证），可放心恢复。
			lg.Info("检测到账号已离线，自动恢复挂机", "account", name, "uid", targetUID, "probe", accName)
			return true
		}
		// 目标在线（用户在游戏中）：继续等待，绝不抢占。
		select {
		case <-time.After(probeInterval):
		case <-ctx.Done():
			return false
		}
	}
}

// pickProbeSession 从当前**正在挂机（online）**的账号中**随机**挑选一个作为在线侦查账号，
// 返回其会话与账号名；无可用账号时返回 (nil, "")。
//
// exclude：必须排除的账号名（通常是被顶下线的目标自己，不能自己侦查自己）。
// failed：  本轮已尝试且失败、需要跳过的账号名集合（侦查会话失效时轮换用）。
//
// 为什么必须随机而不是取第一个（2026-08-31 改）：
//  1. 多账号同时被顶下线时，若固定盯住同一个侦查账号，会把探测请求全部压在
//     它身上，放大风控风险；随机后请求自然分散到所有在线账号。
//  2. 侦查账号失效后重新挑选时，取"第一个"极可能又选回刚失效的那个，
//     导致反复失败；随机 + failed 集合能保证轮换到别的账号。
//  3. 不再依赖账号在配置/state 中的排列顺序，避免列表首账号承担全部探测。
func (m *Manager) pickProbeSession(exclude string, failed map[string]bool) (*session.Session, string) {
	m.mu.Lock()
	defer m.mu.Unlock()

	var sessions []*session.Session
	var names []string
	for name, rt := range m.states {
		if name == exclude {
			continue
		}
		if failed != nil && failed[name] {
			continue
		}
		st := rt.snapshot()
		// 仅"正在挂机中"的账号可充当侦查：其会话有效，且查询为只读不会顶掉用户
		if !st.Running || st.Status != StatusOnline {
			continue
		}
		s := rt.getSession()
		if s == nil {
			continue
		}
		sessions = append(sessions, s)
		names = append(names, name)
	}
	if len(sessions) == 0 {
		return nil, ""
	}
	idx := rand.Intn(len(sessions))
	return sessions[idx], names[idx]
}

// fallbackInterval 无可用在线侦查账号（如全部账号同时被踢下线）时的兜底探测间隔。
// 用户认可此节奏：全掉线时每 10 分钟尝试一次重新登录是合理的（给服务器恢复留足时间）。
const fallbackInterval = 10 * time.Minute

// fallbackWaitRelogin 兜底探测：无可用在线侦查账号时的自动恢复策略。
//
// 每 fallbackInterval：
//  1. 先看看有没有其他账号恢复在线了：有就用它做只读侦查（probeWaitRelogin，零风险）。
//  2. 仍无在线账号：直接 startLocked 重启本账号尝试登录恢复。
//
// 旧实现会"登录另一个 relogin_wait 账号当探针"去查询目标在线状态——多账号同时被踢时，
// 这会导致两个账号互相登录、反复互踢，永远无法稳定恢复（2026-09-01「等待恢复」卡死事故
// 的根因之一）。新实现改为直接重启本账号：第一个恢复的账号随即持有有效会话，下一轮其他
// 被踢账号即可改用零风险只读侦查恢复，互不干扰。
func (m *Manager) fallbackWaitRelogin(ctx context.Context, lg *slog.Logger, rt *accountRuntime, name string, err error) bool {
	lg.Warn("会话失效（可能被其他设备登录），无可用在线侦查账号，转入兜底探测",
		"account", name, "err", err, "interval", fallbackInterval)

	for {
		// 每轮先看看有没有其他账号恢复在线了：有就用它做只读侦查（零风险）。
		if probe, probeName := m.pickProbeSession(name, nil); probe != nil {
			lg.Info("检测到在线侦查账号，改用只读侦查恢复", "account", name, "probe", probeName)
			if ok := m.probeWaitRelogin(ctx, lg, rt, name, probe, probeName); ok {
				return ctx.Err() == nil
			}
			if ctx.Err() != nil {
				return false
			}
			lg.Info("侦查账号不可用，继续兜底探测", "account", name)
		}

		// 仍无任何在线账号可侦查：等待一个间隔后，直接重启本账号尝试恢复。
		// 直接重启（startLocked）而非"登录其他 relogin_wait 账号探测"，避免互相顶号。
		// 若本账号空闲（调试客户端已退出）则登录成功即恢复挂机；若仍被占用则新 goroutine
		// 会再次回到此处重试。第一个恢复的账号会持有会话，供其余账号下一轮只读侦查。
		rt.setStatus(StatusReloginWait, fmt.Sprintf(
			"检测到其他设备登录，挂机已暂停；无在线账号可侦查，每 %s 尝试重新登录恢复",
			fallbackInterval))
		select {
		case <-time.After(fallbackInterval):
		case <-ctx.Done():
			return false
		}

		lg.Info("兜底恢复：直接重启账号重新登录", "account", name)
		// 用 m.startLocked（内部优先基于 rootCtx 派生新 ctx），避免当前账号 ctx
		// 已被取消时新 goroutine 立即退出陷入死循环。
		m.startLocked(ctx, name)
		return ctx.Err() == nil
	}
}


func (rt *accountRuntime) updateUID(sess *session.Session) {
	rt.mu.Lock()
	rt.st.UID = sess.UID
	rt.st.Gate = sess.GateAddr
	rt.sess = sess
	rt.mu.Unlock()
}

// setSession 记录/清除当前登录会话（供其他账号作在线侦查）
func (rt *accountRuntime) setSession(s *session.Session) {
	rt.mu.Lock()
	rt.sess = s
	rt.mu.Unlock()
}

// getSession 返回当前登录会话（可能为 nil）
func (rt *accountRuntime) getSession() *session.Session {
	rt.mu.RLock()
	defer rt.mu.RUnlock()
	return rt.sess
}

func sleepCtx(ctx context.Context, d time.Duration) bool {
	select {
	case <-time.After(d):
		return true
	case <-ctx.Done():
		return false
	}
}

func minDur(a, b time.Duration) time.Duration {
	if a < b {
		return a
	}
	return b
}
