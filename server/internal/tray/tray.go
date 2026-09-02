//go:build windows

// 系统托盘封装（仅 Windows，兼容 Windows 7+）。
// 提供托盘图标、左键双击打开控制台、右键菜单（打开控制台 / 全部启动 / 全部停止 / 退出）。
package tray

import (
	"context"
	_ "embed"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/lxn/walk"

	"youjuhang/internal/core"
)

//go:embed app.ico
var appIconData []byte

// Tray 封装 Windows 系统托盘图标。
type Tray struct {
	app    *walk.Application
	mw     *walk.MainWindow
	ni     *walk.NotifyIcon
	mgr    *core.Manager
	url    string
	onQuit func()
}

// New 创建系统托盘，必须在主 goroutine 中调用。
// mgr 用于菜单的启停操作，url 为 Web 控制台地址，onQuit 在用户选择"退出"时回调。
func New(mgr *core.Manager, url string, onQuit func()) (*Tray, error) {
	trace("tray: walk.App")
	app := walk.App()
	trace("tray: NewMainWindow")
	mw, err := walk.NewMainWindow()
	if err != nil {
		trace("tray: NewMainWindow err: " + err.Error())
		return nil, err
	}
	trace("tray: NewMainWindow ok")
	ni, err := walk.NewNotifyIcon(mw)
	if err != nil {
		trace("tray: NewNotifyIcon err: " + err.Error())
		mw.Dispose()
		return nil, err
	}
	trace("tray: NewNotifyIcon ok")
	t := &Tray{app: app, mw: mw, ni: ni, mgr: mgr, url: url, onQuit: onQuit}
	if err := t.setup(); err != nil {
		trace("tray: setup err: " + err.Error())
		_ = ni.Dispose()
		mw.Dispose()
		return nil, err
	}
	trace("tray: setup ok")
	return t, nil
}

// Run 运行消息循环，阻塞直到 Exit 被调用。
func (t *Tray) Run() int {
	return t.mw.Run()
}

// Exit 结束消息循环。
func (t *Tray) Exit() {
	t.app.Exit(0)
}

// Shutdown 释放托盘资源。
func (t *Tray) Shutdown() {
	if t.ni != nil {
		_ = t.ni.Dispose()
		t.ni = nil
	}
	if t.mw != nil {
		t.mw.Dispose()
		t.mw = nil
	}
	_ = os.Remove(iconTempPath())
}

func (t *Tray) setup() error {
	// 图标加载：优先从 exe 同目录 app.ico 文件加载（最可靠），
	// 其次尝试嵌入资源（rsrc -ico id 1），最后回退系统应用图标。
	icon := t.loadIcon()
	if icon == nil {
		icon = walk.IconApplication()
	}
	if err := t.ni.SetIcon(icon); err != nil {
		return err
	}
	// 关键：walk 的 NotifyIcon 创建时默认隐藏（NIS_HIDDEN），
	// 必须显式 SetVisible(true) 才会在系统托盘显示角标。
	if err := t.ni.SetVisible(true); err != nil {
		return err
	}
	if err := t.ni.SetToolTip("游聚挂机运行中"); err != nil {
		return err
	}
	_ = t.ni.ShowInfo("游聚挂机", "程序已在后台运行，双击此图标打开控制台")

	// 左键双击托盘图标：打开控制台
	t.ni.MouseDown().Attach(func(_ int, _ int, button walk.MouseButton) {
		if button == walk.LeftButton {
			t.openConsole()
		}
	})

	cm := t.ni.ContextMenu()
	add := func(text string, fn func()) {
		act := walk.NewAction()
		_ = act.SetText(text)
		act.Triggered().Attach(fn)
		_ = cm.Actions().Add(act)
	}
	add("打开控制台", t.openConsole)
	add("全部启动", t.startAll)
	add("全部停止", t.stopAll)
	add("退出", t.quit)
	return nil
}

func (t *Tray) openConsole() {
	_ = exec.Command("rundll32", "url.dll,FileProtocolHandler", t.url).Start()
}

func (t *Tray) startAll() {
	t.mgr.StartAll(context.Background())
}

func (t *Tray) stopAll() {
	t.mgr.StopAll()
}

func (t *Tray) quit() {
	if t.onQuit != nil {
		t.onQuit()
	}
	t.Exit()
}

// iconTempPath 返回图标临时文件路径。
func iconTempPath() string {
	return filepath.Join(os.TempDir(), "youjuhang_icon.ico")
}

// trace 追加一行启动跟踪到 exe 目录 startup_error.txt（与 main 的 trace 共用），
// 用于定位 GUI 模式下程序崩溃时执行到了哪一步。
func trace(step string) {
	exe, err := os.Executable()
	dir := "."
	if err == nil {
		dir = filepath.Dir(exe)
	}
	f, err := os.OpenFile(filepath.Join(dir, "startup_error.txt"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0644)
	if err != nil {
		return
	}
	_, _ = f.WriteString(step + "\n")
	_ = f.Close()
}

// loadIcon 加载自定义图标。图标数据通过 //go:embed 打包进二进制，
// 运行时写入临时文件后用 walk.NewIconFromFile 加载（文件加载比资源加载更可靠），
// 程序退出时自动清理临时文件。
func (t *Tray) loadIcon() walk.Image {
	// 1) 从嵌入数据写入临时文件并加载
	if len(appIconData) > 0 {
		tmp := iconTempPath()
		if err := os.WriteFile(tmp, appIconData, 0644); err == nil {
			if icon, err := walk.NewIconFromFile(tmp); err == nil {
				return icon
			}
		}
	}
	// 2) 尝试嵌入资源（rsrc -ico id 1，兼容旧方式）
	if icon, err := walk.NewIconFromResourceId(1); err == nil {
		return icon
	}
	return nil
}
