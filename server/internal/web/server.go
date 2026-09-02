// Package web 提供本地 Web 控制台：多账号管理、状态监控、日志查看
// 前端为内嵌单页面（兼容 Win7 常见浏览器：Chrome 109 / Firefox ESR / IE11 基础渲染）
package web

import (
	"context"
	_ "embed"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strconv"
	"strings"
	"time"

	"youjuhang/internal/config"
	"youjuhang/internal/core"
)

//go:embed ui/index.html
var indexHTML []byte

// Server 是 Web 控制台服务
type Server struct {
	mgr *core.Manager
	srv *http.Server
}

// New 创建 Web 控制台
func New(addr string, mgr *core.Manager) *Server {
	s := &Server{mgr: mgr}
	mux := http.NewServeMux()
	mux.HandleFunc("/", s.handleIndex)
	mux.HandleFunc("/api/status", s.handleStatus)
	mux.HandleFunc("/api/config", s.handleConfig)
	mux.HandleFunc("/api/accounts", s.handleAccounts)
	mux.HandleFunc("/api/accounts/", s.handleAccountItem)
	mux.HandleFunc("/api/start-all", s.handleStartAll)
	mux.HandleFunc("/api/stop-all", s.handleStopAll)
	mux.HandleFunc("/api/logs/", s.handleLogs)
	s.srv = &http.Server{
		Addr:              addr,
		Handler:           mux,
		ReadHeaderTimeout: 10 * time.Second,
	}
	return s
}

// Start 启动 HTTP 服务（异步，返回启动错误或 nil）
func (s *Server) Start() error {
	errCh := make(chan error, 1)
	go func() {
		if err := s.srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-time.After(150 * time.Millisecond):
		return nil
	}
}

// Addr 返回监听地址
func (s *Server) Addr() string { return s.srv.Addr }

func (s *Server) handleIndex(w http.ResponseWriter, r *http.Request) {
	if r.URL.Path != "/" {
		http.NotFound(w, r)
		return
	}
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	_, _ = w.Write(indexHTML)
}

func (s *Server) handleStatus(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	writeJSON(w, s.mgr.Snapshot())
}

// globalConfigBody 是全局配置的 JSON 交换结构
type globalConfigBody struct {
	RefreshMinutes  int  `json:"refresh_minutes"`
	CheckInEnabled  bool `json:"check_in_enabled"`
	AutoClaim       bool `json:"auto_claim"`
	MallEnabled     bool `json:"mall_enabled"`
	RoomHangEnabled bool `json:"room_hang_enabled"`
	LogConsole      bool `json:"log_console"`
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		c := s.mgr.GlobalConfig()
		writeJSON(w, globalConfigBody{
			RefreshMinutes:  c.RefreshMinutes,
			CheckInEnabled:  c.CheckInEnabled,
			AutoClaim:       c.AutoClaim,
			MallEnabled:     c.MallEnabled,
			RoomHangEnabled: c.RoomHangEnabled,
			LogConsole:      c.LogConsole,
		})
	case http.MethodPut:
		var b globalConfigBody
		if err := json.NewDecoder(r.Body).Decode(&b); err != nil {
			writeErr(w, http.StatusBadRequest, "无效的 JSON: "+err.Error())
			return
		}
		if b.RefreshMinutes < 0 || b.RefreshMinutes > 60 {
			writeErr(w, http.StatusBadRequest, "刷新间隔须在 0-60 分钟之间")
			return
		}
		err := s.mgr.UpdateGlobal(func(c *config.Config) {
			c.RefreshMinutes = b.RefreshMinutes
			c.CheckInEnabled = b.CheckInEnabled
			c.AutoClaim = b.AutoClaim
			c.MallEnabled = b.MallEnabled
			c.RoomHangEnabled = b.RoomHangEnabled
			c.LogConsole = b.LogConsole
		})
		if err != nil {
			writeErr(w, http.StatusInternalServerError, err.Error())
			return
		}
		writeJSON(w, map[string]string{"ok": "true"})
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

// accountBody 新增/编辑账号的请求体。
// 在 config.Account 字段基础上附带"天数"参数（JSON 中平级，嵌入结构体自动提升字段）。
type accountBody struct {
	config.Account
	Days    int `json:"days"`     // 新增时：挂机天数（>0 生效；<=0 表示永久）
	AddDays int `json:"add_days"` // 编辑时：在原到期日上累加的天数（>0 生效）
}

// startBody 启动单个账号的请求体
type startBody struct {
	Force bool `json:"force"` // true=忽略过期强制启动（用户已在 UI 确认）
}

// maxDaysInput 天数输入的合理上限（约 10 年），防止手滑输入天文数字
const maxDaysInput = 3650

// checkDays 校验天数：必须是整数且在合理范围内。
// allowNegative=true 时允许负数（编辑场景：减少天数）。
func checkDays(d int, allowNegative bool) error {
	if d > maxDaysInput || d < -maxDaysInput {
		return fmt.Errorf("天数超出合理范围（%d ~ %d）", -maxDaysInput, maxDaysInput)
	}
	if !allowNegative && d < 0 {
		return errors.New("挂机天数不能为负数")
	}
	return nil
}

// decodeBody 解析 JSON 请求体；天数填小数时 Go 会报 unmarshal 错误，
// 这里转成友好提示（"天数必须是整数"）。
func decodeBody(r *http.Request, dst interface{}) error {
	if err := json.NewDecoder(r.Body).Decode(dst); err != nil {
		if strings.Contains(err.Error(), "cannot unmarshal number") {
			return errors.New("天数必须是整数")
		}
		return fmt.Errorf("无效的 JSON: %w", err)
	}
	return nil
}

func (s *Server) handleAccounts(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	var b accountBody
	if err := decodeBody(r, &b); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if b.Name == "" || b.Password == "" {
		writeErr(w, http.StatusBadRequest, "账号名和密码不能为空")
		return
	}
	// 新增场景：天数 >= 0（留空/0 = 长期挂机），不接受负数
	if err := checkDays(b.Days, false); err != nil {
		writeErr(w, http.StatusBadRequest, err.Error())
		return
	}
	if err := s.mgr.AddAccount(b.Account, b.Days); err != nil {
		writeErr(w, http.StatusConflict, err.Error())
		return
	}
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handleAccountItem(w http.ResponseWriter, r *http.Request) {
	rest := strings.TrimPrefix(r.URL.Path, "/api/accounts/")
	parts := strings.SplitN(rest, "/", 2)
	name, err := url.PathUnescape(parts[0])
	if err != nil {
		writeErr(w, http.StatusBadRequest, "无效的账号名")
		return
	}
	action := ""
	if len(parts) == 2 {
		action = parts[1]
	}
	switch r.Method {
	case http.MethodPut:
		var b accountBody
		if err := decodeBody(r, &b); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		// 编辑场景：允许负数（正数=延长，负数=缩短），留空/0 = 保持不变
		if err := checkDays(b.AddDays, true); err != nil {
			writeErr(w, http.StatusBadRequest, err.Error())
			return
		}
		b.Name = name
		pendingRestart, err := s.mgr.UpdateAccount(name, b.Account, b.AddDays)
		if err != nil {
			writeErr(w, http.StatusConflict, err.Error())
			return
		}
		// pendingRestart：运行中账号改了密码/MAC，配置已落盘但当前会话仍用旧凭据，
		// 需重启账号才生效。前端据此追加提示，避免用户误以为改动没保存。
		writeJSON(w, map[string]any{"ok": "true", "pending_restart": pendingRestart})
	case http.MethodDelete:
		if err := s.mgr.RemoveAccount(name); err != nil {
			writeErr(w, http.StatusNotFound, err.Error())
			return
		}
		writeJSON(w, map[string]string{"ok": "true"})
	case http.MethodPost:
		switch action {
		case "start":
			// 注意：不能用 r.Context()——HTTP 请求返回后上下文会被取消，
			// 导致刚启动的账号立即退出。账号生命周期应独立于请求。
			var sb startBody
			// body 可选：老前端/无参调用时不带 body，跳过解析即可
			if r.ContentLength > 0 {
				if err := json.NewDecoder(r.Body).Decode(&sb); err != nil {
					writeErr(w, http.StatusBadRequest, "无效的 JSON: "+err.Error())
					return
				}
			}
			var err error
			if sb.Force {
				err = s.mgr.StartAccountForce(context.Background(), name)
			} else {
				err = s.mgr.StartAccount(context.Background(), name)
			}
			if err != nil {
				// 账号已过期：返回 409 + expired 标记，前端弹确认后带 force=true 重试
				if errors.Is(err, core.ErrAccountExpired) {
					writeJSONStatus(w, http.StatusConflict, map[string]interface{}{
						"error":   err.Error(),
						"expired": true,
					})
					return
				}
				writeErr(w, http.StatusNotFound, err.Error())
				return
			}
			writeJSON(w, map[string]string{"ok": "true"})
		case "stop":
			s.mgr.StopAccount(name)
			writeJSON(w, map[string]string{"ok": "true"})
		case "enable", "disable":
			// 启用/禁用。启用入口是必需的：账号被自动禁用（连续登录被拒）后，
			// 用户改对密码需要能重新启用，否则只能手动编辑 accounts.yaml。
			if err := s.mgr.SetEnabled(name, action == "enable"); err != nil {
				writeErr(w, http.StatusConflict, err.Error())
				return
			}
			writeJSON(w, map[string]string{"ok": "true"})
		default:
			writeErr(w, http.StatusNotFound, "unknown action")
		}
	default:
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleStartAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	// 不能用 r.Context()：HTTP 请求返回后上下文会被取消，
	// 导致本次启动的所有账号立即退出。账号生命周期必须独立于请求。
	skipped := s.mgr.StartAll(context.Background())
	writeJSON(w, map[string]interface{}{"ok": "true", "skipped_expired": skipped})
}

func (s *Server) handleStopAll(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	s.mgr.StopAll()
	writeJSON(w, map[string]string{"ok": "true"})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	name := strings.TrimPrefix(r.URL.Path, "/api/logs/")
	un, err := url.PathUnescape(name)
	if err != nil {
		writeErr(w, http.StatusBadRequest, "invalid name")
		return
	}
	lines := 100
	if v := r.URL.Query().Get("lines"); v != "" {
		if n, e := strconv.Atoi(v); e == nil && n > 0 && n <= 2000 {
			lines = n
		}
	}
	path, ok := s.mgr.LogPath(un)
	if !ok {
		writeErr(w, http.StatusNotFound, "account not found")
		return
	}
	body := map[string]interface{}{"name": un, "lines": tailLines(path, lines)}
	writeJSON(w, body)
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// writeJSONStatus 带自定义 HTTP 状态码的 JSON 响应。
// 用于需要业务标记字段的场景（如 409 + expired=true，前端据此弹确认框）。
func writeJSONStatus(w http.ResponseWriter, status int, v interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, status int, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(map[string]string{"error": msg})
}

// tailLines 读取文件最后 n 行（兼容 UTF-8 边界）
func tailLines(path string, n int) []string {
	if n <= 0 {
		n = 100
	}
	f, err := os.Open(path)
	if err != nil {
		return []string{"[无日志文件: " + path + "]"}
	}
	defer f.Close()
	fi, err := f.Stat()
	if err != nil {
		return []string{"[日志读取失败]"}
	}
	const chunk = 16 * 1024
	offset := fi.Size()
	var data []byte
	for offset > 0 && len(data) < chunk*64 {
		read := int64(chunk)
		if offset < read {
			read = offset
		}
		offset -= read
		buf := make([]byte, read)
		if _, err := f.ReadAt(buf, offset); err != nil && err.Error() != "EOF" {
			break
		}
		data = append(buf, data...)
		if strings.Count(string(data), "\n") > n {
			break
		}
	}
	lines := strings.Split(string(data), "\n")
	if len(lines) > n {
		lines = lines[len(lines)-n:]
	}
	// 丢弃可能被截断的首行（非文件头时）
	if len(data) > 0 && data[0] != '\n' {
		if lines[0] != "" && !strings.Contains(lines[0], "time=") && len(lines) > 1 {
			lines = lines[1:]
		}
	}
	return lines
}
