package main

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestContainsFlag 识别命令行参数的各种写法（用于避免重复追加 -no-browser）
func TestContainsFlag(t *testing.T) {
	cases := []struct {
		args []string
		name string
		want bool
	}{
		{[]string{"-config", "a.yaml"}, "no-browser", false},
		{[]string{"-no-browser"}, "no-browser", true},
		{[]string{"--no-browser"}, "no-browser", true},
		{[]string{"-no-browser=true"}, "no-browser", true},
		{[]string{"-nobrowser"}, "no-browser", false}, // 不允许拼错，防误判
		{[]string{}, "no-browser", false},
		{[]string{"-config", "a.yaml"}, "config", true},
	}
	for _, c := range cases {
		if got := containsFlag(c.args, c.name); got != c.want {
			t.Errorf("containsFlag(%v, %q) = %v, want %v", c.args, c.name, got, c.want)
		}
	}
}

// TestProcessAliveSelf 自身进程必然存活；非法 PID 必然不存活
func TestProcessAliveSelf(t *testing.T) {
	if !processAlive(os.Getpid()) {
		t.Error("processAlive(自身PID) 应返回 true")
	}
	for _, bad := range []int{0, -1} {
		if processAlive(bad) {
			t.Errorf("processAlive(%d) 应返回 false", bad)
		}
	}
}

// TestProcessAliveDetectsExit 进程退出后必须判定为不存活。
//
// 这是本守护进程最关键的正确性要求：Windows 上 os.FindProcess 对已退出的
// 进程仍可能返回成功，若检测不到"已死"，守护进程会把崩溃误判为假死，
// 白白多等一轮确认，还会去 Kill 一个已死的进程（Access is denied）。
func TestProcessAliveDetectsExit(t *testing.T) {
	if testing.Short() {
		t.Skip("需要启动子进程")
	}
	self, err := os.Executable()
	if err != nil {
		t.Skip("无法获取自身路径")
	}
	// 启动测试二进制自身，用不存在的测试名让它立刻跑完退出
	cmd := exec.Command(self, "-test.run=TestHelperNeverRuns")
	if err := cmd.Start(); err != nil {
		t.Skip("无法启动子进程: ", err)
	}
	pid := cmd.Process.Pid
	// 子进程启动后应判定存活
	if !processAlive(pid) {
		_ = cmd.Process.Kill()
		t.Fatal("刚启动的子进程应判定为存活")
	}
	_ = cmd.Process.Kill()
	_, _ = cmd.Process.Wait()
	// 退出后应判定为不存活
	if processAlive(pid) {
		t.Errorf("进程 %d 已退出，processAlive 仍返回 true（会把崩溃误判为假死）", pid)
	}
}

// TestLoadState 存活标记文件的读写往返
func TestLoadState(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "run_state.json")
	want := &runState{
		PID:       4321,
		Exe:       `C:\youjuhang\youjuhang-win64.exe`,
		Args:      []string{"-config", "accounts.yaml"},
		Started:   "2026-09-01 10:00:00",
		LastAlive: "2026-09-01 10:05:00",
	}
	raw, err := json.Marshal(want)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0644); err != nil {
		t.Fatal(err)
	}
	got, err := loadState(path)
	if err != nil {
		t.Fatal(err)
	}
	if got.PID != want.PID || got.Exe != want.Exe || got.LastAlive != want.LastAlive {
		t.Errorf("往返不一致: got %+v want %+v", got, want)
	}
	if len(got.Args) != len(want.Args) || got.Args[0] != want.Args[0] {
		t.Errorf("args 往返不一致: got %v want %v", got.Args, want.Args)
	}

	// 不存在的文件必须返回 os.IsNotExist，供主循环判定"优雅退出"
	if _, err := loadState(filepath.Join(dir, "nope.json")); !os.IsNotExist(err) {
		t.Errorf("文件不存在时应返回 IsNotExist，实际: %v", err)
	}
}

// TestParseLastAlive 心跳时间戳解析（与写入格式保持一致）
func TestParseLastAlive(t *testing.T) {
	s := "2026-09-01 10:05:00"
	ts, err := time.ParseInLocation("2006-01-02 15:04:05", s, time.Local)
	if err != nil {
		t.Fatalf("解析失败（格式与主程序写入不一致）: %v", err)
	}
	if ts.Hour() != 10 || ts.Minute() != 5 || ts.Day() != 1 {
		t.Errorf("解析结果错误: %v", ts)
	}
}

// TestDecideCrashPIDGone 进程已消失 = 明确崩溃，无论心跳是否新鲜都立即返回 actionCrash。
// 这是 2026-09-01 事故的核心修复：死进程不必等 staleAfter=5min。
func TestDecideCrashPIDGone(t *testing.T) {
	st := &runState{PID: -1} // processAlive(-1) 返回 false
	now := time.Now()
	if a := decide(st, now.Add(-30*time.Second), now, actionWait); a != actionCrash {
		t.Errorf("心跳仅过期 30s、进程已没，应立即崩溃，实际 %v", a)
	}
	if a := decide(st, now, now, actionWait); a != actionCrash {
		t.Errorf("心跳新鲜、进程已没，仍应崩溃，实际 %v", a)
	}
}

// TestDecideWait 进程在 + 心跳新鲜 = 正常
func TestDecideWait(t *testing.T) {
	prev := staleAfter
	staleAfter = 5 * time.Minute
	defer func() { staleAfter = prev }()

	st := &runState{PID: os.Getpid()}
	now := time.Now()
	if a := decide(st, now.Add(-30*time.Second), now, actionWait); a != actionWait {
		t.Errorf("进程在 + 心跳新鲜应 wait，实际 %v", a)
	}
}

// TestDecideSuspectThenConfirm 进程在 + 心跳过期：首次确认、二次杀重启
func TestDecideSuspectThenConfirm(t *testing.T) {
	prev := staleAfter
	staleAfter = 5 * time.Minute
	defer func() { staleAfter = prev }()

	st := &runState{PID: os.Getpid()}
	now := time.Now()
	old := now.Add(-10 * time.Minute) // 心跳已过期 staleAfter=5min
	// 上一轮是 wait → 本轮首次进入 suspectHang
	if a := decide(st, old, now, actionWait); a != actionSuspectHang {
		t.Errorf("首次心跳过期应 suspectHang，实际 %v", a)
	}
	// 上一轮是 suspectHang → 本轮确认假死，杀+重启
	if a := decide(st, old, now, actionSuspectHang); a != actionConfirmHang {
		t.Errorf("连续两次心跳过期应 confirmHang，实际 %v", a)
	}
	// 上一轮是 confirmHang（已处理） → 心跳恢复应回到 wait
	if a := decide(st, now.Add(-time.Second), now, actionConfirmHang); a != actionWait {
		t.Errorf("心跳恢复后应回到 wait，实际 %v", a)
	}
}
