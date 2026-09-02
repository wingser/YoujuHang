//go:build windows

package main

import (
	"crypto/rand"
	"syscall"
	"unsafe"
)

// init 在 Windows 上把 crypto/rand.Reader 换成 RtlGenRandom 兜底实现。
//
// 背景（2026-09-02 连续崩溃事故复盘）：
// Go（含 1.21）的 crypto/rand 在 Windows 上直接调用 bcryptprimitives!ProcessPrng。
// 而 Win7 / 老版本 Windows Server 的 bcryptprimitives.dll **不导出** ProcessPrng，
// 于是任何 crypto/rand.Read 调用（golang.org/x/net/http2 流重置时必调用）都会
// 触发 mustFind panic 且不可 recover —— 进程"无声消失"，由 guard 拉起后再崩。
//
// 关键事实：同版本 Go 的 runtime.getRandomData 在 ProcessPrng 缺失时已内置
// 回退到 advapi32!RtlGenRandom（SystemFunction036，XP 起即存在）。这里照搬该
// 回退：在 main.init 阶段（早于任何 crypto/rand.Read）替换 Reader，于是 http2
// 等所有 crypto/rand 调用在老系统上也能正常工作，不再 panic。
//
// 替换时机：crypto/rand 包自身的 init 会把 Reader 设为 rngReader（用 ProcessPrng）；
// 但 main 包的 init 在依赖包 init 之后执行，因此本 init 的赋值最后生效。
func init() {
	rand.Reader = &rtlGenRandomReader{}
}

// rtlGenRandomReader 用 advapi32!RtlGenRandom（导出名 SystemFunction036）填充随机数据。
// RtlGenRandom 自 Windows XP 起就存在于 advapi32.dll，不受 ProcessPrng 缺失影响。
type rtlGenRandomReader struct{}

func (rtlGenRandomReader) Read(b []byte) (int, error) {
	if len(b) == 0 {
		return 0, nil
	}
	r1, _, _ := syscall.Syscall(rtlGenRandomProc.Addr(), 2,
		uintptr(unsafe.Pointer(&b[0])), uintptr(len(b)), 0)
	if r1 == 0 {
		return 0, syscall.EINVAL
	}
	return len(b), nil
}

var (
	advapi32         = syscall.NewLazyDLL("advapi32.dll")
	rtlGenRandomProc = advapi32.NewProc("SystemFunction036")
)

// testRtlGenRandom 验证兜底随机源是否可用（给 checkWindowsRNG 用）。
func testRtlGenRandom() error {
	buf := make([]byte, 8)
	_, err := rtlGenRandomReader{}.Read(buf)
	return err
}
