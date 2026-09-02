package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"testing"
	"time"
)

// TestHelperSleep 供 TestStopGuardKillsGuard 当作"假守护进程"使用的睡眠助手。
// 通过环境变量 YJH_TEST_SLEEP 指定睡眠时长；未设置时跳过。
func TestHelperSleep(t *testing.T) {
	d := os.Getenv("YJH_TEST_SLEEP")
	if d == "" {
		t.Skip("helper: 需要 YJH_TEST_SLEEP")
	}
	dur, err := time.ParseDuration(d)
	if err != nil {
		t.Skip("helper: 非法时长")
	}
	time.Sleep(dur)
}

// TestStopGuardKillsGuard 主程序退出时必须主动结束守护进程。
//
// 背景（2026-09-01 用户反馈）：主程序退出时若只删除 run_state.json，
// 守护进程要等下一次轮询（默认 15s）才发现文件消失才退出，
// 期间用户会看到"主程序退出了但 guard 还在"，甚至可能被重新拉起。
func TestStopGuardKillsGuard(t *testing.T) {
	if testing.Short() {
		t.Skip("需要启动子进程")
	}
	// 用测试二进制自身当"假守护进程"（自包含，不依赖外部命令）
	cmd := exec.Command(os.Args[0], "-test.run=TestHelperSleep")
	cmd.Env = append(os.Environ(), "YJH_TEST_SLEEP=120s")
	if err := cmd.Start(); err != nil {
		t.Skip("无法启动子进程: ", err)
	}
	pid := cmd.Process.Pid

	pidFile := filepath.Join(exeDir(), guardPIDFileName)
	if err := os.WriteFile(pidFile, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		_ = cmd.Process.Kill()
		t.Fatal(err)
	}
	defer os.Remove(pidFile)

	stopGuard()

	// 进程应被结束：Wait() 在子进程退出后返回。用超时避免测试卡死。
	done := make(chan struct{})
	go func() {
		_ = cmd.Wait()
		close(done)
	}()
	select {
	case <-done:
		// 符合预期
	case <-time.After(10 * time.Second):
		t.Errorf("stopGuard() 之后进程 %d 仍在运行——守护进程关闭不了", pid)
		_ = cmd.Process.Kill()
		<-done
	}

	// PID 文件应被清理
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("stopGuard() 之后 guard.pid 应被删除")
	}
}

// TestStopGuardNoPIDFile PID 文件不存在时应静默返回（不能报错/崩溃）
func TestStopGuardNoPIDFile(t *testing.T) {
	_ = os.Remove(filepath.Join(exeDir(), guardPIDFileName))
	stopGuard()
}

// TestStopGuardInvalidPIDFile PID 文件内容非法时应清理并静默返回
func TestStopGuardInvalidPIDFile(t *testing.T) {
	pidFile := filepath.Join(exeDir(), guardPIDFileName)
	if err := os.WriteFile(pidFile, []byte("not-a-number"), 0o644); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(pidFile)
	stopGuard()
	if _, err := os.Stat(pidFile); err == nil {
		t.Error("非法 guard.pid 应被清理")
	}
}
