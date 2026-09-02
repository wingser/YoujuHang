//go:build !windows

// 非 Windows 平台的托盘占位实现，保证跨平台编译通过。
package tray

import (
	"errors"

	"youjuhang/internal/core"
)

// Tray 仅 Windows 平台有实际实现。
type Tray struct{}

// New 在非 Windows 平台返回错误。
func New(_ *core.Manager, _ string, _ func()) (*Tray, error) {
	return nil, errors.New("系统托盘仅支持 Windows")
}

// Run 空操作。
func (t *Tray) Run() error { return nil }

// Exit 空操作。
func (t *Tray) Exit() {}

// Shutdown 空操作。
func (t *Tray) Shutdown() {}
