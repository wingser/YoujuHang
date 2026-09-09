// Package config 加载账号配置（YAML）
package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

// Config 是顶层配置
type Config struct {
	// LoginAddr 登录服务器地址，默认 gamelogin3.gotvg.com:18000
	LoginAddr string `yaml:"login_addr"`
	// LogDir 日志目录
	LogDir string `yaml:"log_dir"`
	// LogConsole 是否同时输出控制台
	LogConsole bool `yaml:"log_console"`
	// RefreshMinutes 在线保持刷新间隔（分钟）。
	// 未配置（0 或负）时由 biz.Worker.refreshInterval() 兜底为 10 分钟，
	// 实际请求间隔在此基础上带 ±20% 随机抖动（风控规避）。
	RefreshMinutes int `yaml:"refresh_minutes"`
	// CheckInEnabled 是否启用每日签到
	CheckInEnabled bool `yaml:"check_in_enabled"`
	// AutoClaim 是否自动领取任务奖励（在线时长/签到天数等可领任务）
	AutoClaim bool `yaml:"auto_claim"`
	// MallEnabled 是否启用商城礼包领取（每月 1-7 日微信绑定礼包）
	MallEnabled bool `yaml:"mall_enabled"`
	// AvatarEnabled 是否自动佩戴「经验加成最高」的头像装扮。
	//
	// 每天检查一次：拉取个人空间装扮列表，选出加成最高（additional_exp）且未过期的
	// 头像佩戴；已经戴着最优头像时不再重复请求。协议见 internal/mall/dress.go。
	//
	// 注意：加成型头像多绑定具体页游角色（如「维京传奇 144服 420级」），
	// 账号在该页游无角色/等级不足时会佩戴失败，日志会记录原因。
	AvatarEnabled bool `yaml:"avatar_enabled"`
	// RoomHangEnabled 是否启用房间挂机（进入自由区音乐房保持在线，累计房间在线时长）。
	// 挂机区 ID 固定为 room.HangZoneID（=1 自由区），已写死在代码里，不提供 room_hang_zone 配置项。
	RoomHangEnabled bool `yaml:"room_hang_enabled"`
	// ContributeEnabled 是否启用战队贡献捐献（每日捐献贡献度）
	ContributeEnabled bool `yaml:"contribute_enabled"`
	// ContributeDaily 每日捐献贡献度目标，默认 3000
	ContributeDaily int `yaml:"contribute_daily"`
	// ContributeMinBalance 贡献度余额下限，低于此值不做捐献，默认 3000
	ContributeMinBalance int `yaml:"contribute_min_balance"`
	// ContributeStep 单次捐献量，默认 1000
	ContributeStep int `yaml:"contribute_step"`
	// WebAddr Web 控制台监听地址，默认 127.0.0.1:29090（仅本机可访问）。
	// 命令行 -web 参数可覆盖本配置。请勿改成 0.0.0.0 / 局域网 IP，否则控制台会暴露到网络。
	WebAddr string `yaml:"web_addr"`
	// Accounts 账号列表
	Accounts []Account `yaml:"accounts"`
}

// Account 是单个账号配置
type Account struct {
	// Name 登录账号
	Name string `yaml:"name"`
	// Password 明文密码（程序计算 MD5 后发送）
	Password string `yaml:"password"`
	// MAC 网卡 MAC（格式 XX-XX-XX-XX-XX-XX）
	MAC string `yaml:"mac"`
	// Enabled 是否启用
	Enabled *bool `yaml:"enabled"`
	// ExpireDate 挂机到期日，格式 "2006-01-02"（YYYY-MM-DD）。
	// 空字符串表示永久有效。
	//
	// 到期语义（按**日期**比较，不比较时刻）：
	//   当日日期 <= ExpireDate → 未过期，正常挂机（到期日当天仍可挂机）
	//   当日日期 >  ExpireDate → 已过期，不自动启动、运行中会被自动停止
	// 例：今天 2026-08-31 填 3 天 → ExpireDate=2026-09-03，
	//     8/31、9/1、9/2、9/3 均可挂机，9/4 起过期。
	ExpireDate string `yaml:"expire_date,omitempty"`
}

// ExpireDateLayout 到期日的日期格式（仅日期，粒度到天）
const ExpireDateLayout = "2006-01-02"

// CalcExpireDate 按"从今天起 N 天"计算到期日。
// days<=0 返回空字符串（表示永久有效）。
// 例：今天 2026-08-31，days=3 → "2026-09-03"。
func CalcExpireDate(now time.Time, days int) string {
	if days <= 0 {
		return ""
	}
	return now.AddDate(0, 0, days).Format(ExpireDateLayout)
}

// ExtendExpireDate 调整挂机到期日。
//
// 基数取「原到期日」还是「今天」，取决于账号当前是否已过期（2026-09-02 需求调整）：
//
//	| 原到期日状态     | days > 0     | days == 0         | days < 0     |
//	|-----------------|--------------|-------------------|--------------|
//	| 未过期（含当天） | 原到期日+days | 原到期日（不变）  | 原到期日+days |
//	| 已过期           | 今天+days    | 今天（拉回今天）  | 原到期日+days |
//	| 永久/格式非法    | 今天+days    | 原样返回          | 原样返回      |
//
// 设计理由：
//  1. 正数续期：未过期时在**原到期日**上累加，不让已购天数蒸发；
//     但**已过期**的账号若从过期日累加，加完仍是过去日期（账号依旧不能用），
//     因此改为以**今天**为基数。例：一周前过期、今天填 1 → 到期日为明天。
//  2. 填 0：把已过期账号的有效期拉回「今天」（今天仍可挂机，明天起过期），
//     未过期账号则保持原到期日不动，避免误伤。
//  3. 负数缩短：一律以**原到期日**为基数向前减，缩短后若早于今天即立即过期。
//  4. 永久账号（空/格式非法）：只能正数设定期限（今天起算），0 与负数均保持永久。
func ExtendExpireDate(old string, today time.Time, days int) string {
	base, err := time.ParseInLocation(ExpireDateLayout, old, today.Location())
	if err != nil {
		// 原到期日为空或格式非法 → 按永久账号处理
		if days > 0 {
			return CalcExpireDate(today, days)
		}
		return old
	}

	// 已过期账号：正数以今天为基数，0 拉回今天，负数仍以原到期日为基数
	if IsExpired(old, today) {
		switch {
		case days > 0:
			return today.AddDate(0, 0, days).Format(ExpireDateLayout)
		case days == 0:
			return today.Format(ExpireDateLayout)
		default:
			return base.AddDate(0, 0, days).Format(ExpireDateLayout)
		}
	}

	// 未过期（含到期日当天）：一律以原到期日为基数；days==0 时自然保持不变
	return base.AddDate(0, 0, days).Format(ExpireDateLayout)
}

// IsExpired 判断到期日是否已过（按日期比较）。
// expireDate 为空或格式非法 → 视为永久有效，返回 false。
func IsExpired(expireDate string, now time.Time) bool {
	if expireDate == "" {
		return false // 永久
	}
	exp, err := time.ParseInLocation(ExpireDateLayout, expireDate, now.Location())
	if err != nil {
		return false // 格式非法，按永久处理（避免误停账号）
	}
	// 仅比较日期：取当天 00:00 与到期日 00:00 比较
	today := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, now.Location())
	return today.After(exp)
}

// DefaultConfig 返回程序内置缺省配置。
//
// 为什么需要（2026-09-02，为"只发布主程序"场景）：
// 发布时可能只带 exe 不带 accounts.yaml。此前 Load 读不到文件即返回错误、
// main 直接 os.Exit(1)，而 -H windowsgui 无控制台，用户双击只看到"闪退"、
// 没有任何提示，体验极差。改为用缺省配置启动：Web 控制台照常可用，
// 用户在页面上添加账号后配置会自动写盘生成。
//
// 缺省原则：
//   - 自动化功能**默认开启**（签到/领奖/商城/挂机/捐献），这是本程序的核心价值；
//   - 网络与监听取最安全值：LoginAddr/WebAddr 留空，
//     由 gate.New 与 main.resolveWebAddr 兜底为官方登录服与 127.0.0.1:29090（仅本机）。
func DefaultConfig() *Config {
	return &Config{
		RefreshMinutes:       10,   // biz 侧还有兜底（<=0 时按 10 分钟）
		CheckInEnabled:       true, // 每日签到
		AutoClaim:            true, // 任务奖励自动领取
		MallEnabled:          true, // 商城礼包（每月 1-7 日）
		AvatarEnabled:        true, // 自动佩戴最高经验加成头像
		RoomHangEnabled:      true, // 战盟房间挂机（仅已加入战队的账号生效）
		ContributeEnabled:    true, // 战队捐献
		ContributeDaily:      3000,
		ContributeMinBalance: 3000,
		ContributeStep:       1000,
		// LoginAddr / WebAddr / LogDir 留空，由下游兜底
	}
}

// Load 从文件加载配置并校验
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	// 必须以 DefaultConfig() 打底再 Unmarshal（2026-09-09 修复）：
	// yaml.Unmarshal 只覆盖文件中**出现**的字段，缺失字段保留原值。
	// 此前直接 Unmarshal 到零值结构，bool 字段（check_in_enabled 等）全变 false——
	// 旧配置文件里恰好显式写了 true 才一直没暴露；新增的 avatar_enabled 不在
	// 旧文件里 → 被解析成 false → 功能静默禁用，日志一条记录都没有。
	// 另注意：web 控制台 Save 会把内存中的（错误）值写回文件，
	// 因此带 bug 版本保存过的配置里可能存在显式 avatar_enabled: false，需手动删除该行。
	cfg := DefaultConfig()
	if err := yaml.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}
	if len(cfg.Accounts) == 0 {
		return nil, fmt.Errorf("no accounts in config")
	}
	for i := range cfg.Accounts {
		a := &cfg.Accounts[i]
		if a.Name == "" {
			return nil, fmt.Errorf("account[%d] missing name", i)
		}
		if a.Password == "" {
			return nil, fmt.Errorf("account %q missing password", a.Name)
		}

		if a.Enabled == nil {
			en := true
			a.Enabled = &en
		}
	}
	// 捐献配置默认值
	if cfg.ContributeDaily <= 0 {
		cfg.ContributeDaily = 3000
	}
	if cfg.ContributeMinBalance <= 0 {
		cfg.ContributeMinBalance = 3000
	}
	if cfg.ContributeStep <= 0 {
		cfg.ContributeStep = 1000
	}
	return cfg, nil
}

// Save 将配置序列化写回文件（供 UI 动态修改账号后持久化）
func (c *Config) Save(path string) error {
	data, err := yaml.Marshal(c)
	if err != nil {
		return fmt.Errorf("marshal config: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}
