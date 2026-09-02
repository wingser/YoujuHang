// 任务状态持久化（按 uid 区分，跨程序重启保留）。
//
// 为什么合并到一个文件（2026-09-01 用户反馈）：
//  此前分两个文件（contribute_state.json、mall_state.json），存在两个问题：
//  1. **并发写竞争**——多账号 goroutine 同时触发 AddContribute / MarkMallClaimed，
//     两个文件分别写，文件系统层面没有事务保证，可能出现一个写成功另一个失败、
//     导致状态不一致；
//  2. **运维繁琐**——备份、迁移、查看都得分别处理两份文件。
//
// 新设计：单一 `task_state.json` 文件，进程内**单实例**互斥锁保护并发写，
// 每次保存前把当前文件复制为 `.bak` 备份（损坏时可手动恢复），
// 并保留对老文件的**自动迁移**（启动时若只有老文件则一次性导入并删除）。
package biz

import (
	"encoding/json"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"
)

var nowFn = time.Now

// stateFileName 新统一的状态文件名（替代原来的 contribute_state.json + mall_state.json）
const stateFileName = "task_state.json"

// StateStoreFileName 导出文件名常量，供 core 层构造路径
func StateStoreFileName() string { return stateFileName }

// stateData 顶层 JSON 结构：当前仅含两类状态（按需扩展）
type stateData struct {
	Contribute contributeData `json:"contribute"`
	Mall       mallData       `json:"mall"`
}

// contributeData 战队贡献捐献当日已捐量（按日期 + uid；跨天清零）
type contributeData struct {
	Date  string         `json:"date"` // 2006-01-02；与 byUID 不匹配时丢弃
	ByUID map[uint32]int `json:"by_uid"`
}

// mallData 月度礼包领取状态（按月份 + uid）
type mallData struct {
	ByUID map[uint32]mallEntry `json:"by_uid"`
}

type mallEntry struct {
	ClaimedMonth     int    `json:"claimed_month,omitempty"`     // 已领取的月份 key
	UnavailableMonth int    `json:"unavailable_month,omitempty"` // 不可领取的月份 key
	LastInfo         string `json:"last_info,omitempty"`         // 最近一次服务器返回（排障用）
}

// StateStore 任务状态持久化（单实例、互斥锁、写时自动备份）
type StateStore struct {
	mu   sync.Mutex
	path string // task_state.json 的绝对路径
	data stateData
}

// NewStateStore 创建并加载已有状态；自动从旧文件迁移（一次性）。
func NewStateStore(path string) *StateStore {
	s := &StateStore{path: path}
	s.load()
	return s
}

// load 读取主文件。兼容四种情况（按优先级）：
//  1. **旧格式内容 + 新文件名**（用户把 contribute_state.json 改名成 task_state.json）
//  2. 新格式（含 contribute/mall 字段）
//  3. 主文件损坏 → 用 .bak 恢复
//  4. 主文件不存在 → 从旧文件 contribute_state.json / mall_state.json 迁移
//
// 为什么第 1 条必须最先判断（2026-09-01 真实事故）：
// Go 的 json.Unmarshal 对**不匹配的顶层字段会静默忽略并返回成功**。
// 旧格式 `{"date":..., "by_uid":{...}}` 被直接解析进 stateData（字段是 contribute/mall）时，
// 结果全是零值却"解析成功" → 捐献记录凭空消失 → ContributeToday 返回 0
// → 程序认为今天还没捐过 → **重复捐献**。这正是用户手动改名想避免、反而被触发的问题。
func (s *StateStore) load() {
	raw, err := os.ReadFile(s.path)
	if err == nil && len(raw) > 0 {
		// ① 旧格式内容（顶层 date / by_uid）
		var legacy struct {
			Date  string         `json:"date"`
			ByUID map[uint32]int `json:"by_uid"`
		}
		if json.Unmarshal(raw, &legacy) == nil && (legacy.Date != "" || len(legacy.ByUID) > 0) {
			s.data.Contribute = legacy
			slog.Info("检测到旧格式状态内容（顶层 date/by_uid），已迁移到新结构",
				"date", legacy.Date, "accounts", len(legacy.ByUID))
			_ = s.saveLocked() // 立即按新格式重写，下次就是新格式
			return
		}

		// ② 新格式
		var data stateData
		if json.Unmarshal(raw, &data) == nil {
			s.data = data
			return
		}

		// ③ 主文件损坏 → 尝试 .bak 恢复
		if bak, berr := os.ReadFile(s.path + ".bak"); berr == nil {
			if jerr := json.Unmarshal(bak, &s.data); jerr == nil {
				slog.Warn("主状态文件损坏，已自动从 .bak 恢复", "path", s.path)
				return
			}
		}
	}

	// ④ 主文件不存在或全部恢复失败 → 从旧文件迁移
	s.migrateFromLegacy()
}

// migrateFromLegacy 从旧 contribute_state.json + mall_state.json 一次性迁移
func (s *StateStore) migrateFromLegacy() {
	dir := filepath.Dir(s.path)
	migrated := false
	if raw, err := os.ReadFile(filepath.Join(dir, "contribute_state.json")); err == nil {
		var old struct {
			Date  string         `json:"date"`
			ByUID map[uint32]int `json:"by_uid"`
		}
		if json.Unmarshal(raw, &old) == nil {
			s.data.Contribute = old
			migrated = true
			_ = os.Remove(filepath.Join(dir, "contribute_state.json"))
		}
	}
	if raw, err := os.ReadFile(filepath.Join(dir, "mall_state.json")); err == nil {
		var old struct {
			ByUID map[uint32]mallEntry `json:"by_uid"`
		}
		if json.Unmarshal(raw, &old) == nil {
			s.data.Mall.ByUID = old.ByUID
			migrated = true
			_ = os.Remove(filepath.Join(dir, "mall_state.json"))
		}
	}
	if migrated {
		_ = s.saveLocked() // 迁移完成后立即写新文件，落盘
	}
}

// save 线程安全的保存入口（取锁 + 备份 + 写新文件）
func (s *StateStore) save() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.saveLocked()
}

// saveLocked 写入前的"读当前 → 备份 .bak → 写新文件"原子序列。
//
// 必须在调用方持锁的情况下调用。备份策略：
//  1. 读当前主文件内容（如有）
//  2. 写入 task_state.json.bak（损坏时可手动恢复）
//  3. 写新内容到主文件
//
// 多个 goroutine 同时写时由 mu 串行化，文件层面只会有一个写入窗口。
func (s *StateStore) saveLocked() error {
	if cur, err := os.ReadFile(s.path); err == nil && len(cur) > 0 {
		_ = os.WriteFile(s.path+".bak", cur, 0o644)
	}
	raw, err := json.MarshalIndent(s.data, "", "  ")
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.path, raw, 0o644)
}

// ResetContributeIfNewDay 跨天清空（由 Worker 在主循环检测到跨天时调用）
func (s *StateStore) ResetContributeIfNewDay() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetContributeIfNewDayLocked()
}

func (s *StateStore) resetContributeIfNewDayLocked() {
	today := todayStr()
	if s.data.Contribute.Date == today {
		return
	}
	if s.data.Contribute.Date != "" {
		slog.Info("战队捐献记录跨天清空", "from", s.data.Contribute.Date, "to", today)
	}
	s.data.Contribute.Date = today
	s.data.Contribute.ByUID = nil
	// 注意：这里不要主动 save——调用方（Add/Today）会统一保存一次。
	// 否则会产生"写两次"的副作用：第一次写空 ByUID（reset 后），第二次写真正的值，
	// 留下一个错误的 .bak 备份。
}

// ContributeToday 返回指定账号当日已捐献量
func (s *StateStore) ContributeToday(uid uint32) int {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetContributeIfNewDayLocked()
	return s.data.Contribute.ByUID[uid]
}

// ContributeAdd 累加指定账号当日已捐献量并持久化
func (s *StateStore) ContributeAdd(uid uint32, amount int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.resetContributeIfNewDayLocked()
	if s.data.Contribute.ByUID == nil {
		s.data.Contribute.ByUID = make(map[uint32]int)
	}
	s.data.Contribute.ByUID[uid] += amount
	return s.saveLocked()
}

// MallIsClaimed 本月是否已领取（用于跳过重复请求 + UI 显示已完成）
func (s *StateStore) MallIsClaimed(uid uint32, monthKey int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Mall.ByUID[uid].ClaimedMonth == monthKey
}

// MallIsUnavailable 本月是否被判定不可领取（如未绑定微信）
func (s *StateStore) MallIsUnavailable(uid uint32, monthKey int) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Mall.ByUID[uid].UnavailableMonth == monthKey
}

// MallMarkClaimed 标记本月已领取
func (s *StateStore) MallMarkClaimed(uid uint32, monthKey int, info string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMallMapLocked()
	e := s.data.Mall.ByUID[uid]
	e.ClaimedMonth = monthKey
	e.UnavailableMonth = 0
	e.LastInfo = info
	s.data.Mall.ByUID[uid] = e
	return s.saveLocked()
}

// MallMarkUnavailable 标记本月不可领取（如未绑定微信）
func (s *StateStore) MallMarkUnavailable(uid uint32, monthKey int, info string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.ensureMallMapLocked()
	e := s.data.Mall.ByUID[uid]
	e.UnavailableMonth = monthKey
	e.LastInfo = info
	s.data.Mall.ByUID[uid] = e
	return s.saveLocked()
}

// ensureMallMapLocked 确保 Mall.ByUID 已初始化（调用方需持锁）。
//
// 为什么必须：新部署时 task_state.json 尚不存在，Mall.ByUID 是 **nil map**。
// 直接 `m[uid] = e` 会 panic: assignment to entry in nil map ——
// 这在 maybeMallClaim 的写入路径上没有任何 recover，会**直接终结进程**
// （2026-09-01 由测试 TestStateStoreMallMarkedUnavailablePersists 暴露）。
func (s *StateStore) ensureMallMapLocked() {
	if s.data.Mall.ByUID == nil {
		s.data.Mall.ByUID = make(map[uint32]mallEntry)
	}
}

// MallLastInfo 返回最近一次服务器 info（排障用）
func (s *StateStore) MallLastInfo(uid uint32) string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data.Mall.ByUID[uid].LastInfo
}

// todayStr 返回本地日期字符串（YYYY-MM-DD）。复用 contribute.go 里 dayKey 的语义。
func todayStr() string {
	now := nowFn()
	return now.Format("2006-01-02")
}