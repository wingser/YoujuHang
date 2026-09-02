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

// Load 从文件加载配置并校验
func Load(path string) (*Config, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}
	var cfg Config
	if err := yaml.Unmarshal(raw, &cfg); err != nil {
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
	return &cfg, nil
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
