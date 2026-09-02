// Package gate 实现 HTTP/2 (h2c 明文) 客户端，覆盖登录与网关业务请求
// 协议详见 docs/protocol_notes.md（实测验证，2026-08-26）
package gate

import (
	"bytes"
	"compress/zlib"
	"context"
	"crypto/tls"
	"encoding/base64"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"golang.org/x/net/http2"

	"youjuhang/internal/proto"
	"youjuhang/internal/protocol"
)

// DefaultLoginAddr 登录服务器（实测固定）
const DefaultLoginAddr = "gamelogin3.gotvg.com:18000"

// ErrLoginRejected 登录被服务器**明确拒绝**（登录响应 ret != 1）。
//
// 必须与网络/传输类失败区分开：
//   - 传输失败（HTTP 拨号失败、超时、响应 base64 解析失败、缺 token/gate）
//     是**可恢复**的，可能只是网络抖动或服务器临时不可用，应当继续重试；
//   - 被服务器拒绝则说明**凭据有问题**（密码错误、账号不存在、封禁等），
//     重试多少次结果都一样，继续重试只会徒增被风控/限流盯上的风险。
//
// 调用方用 errors.Is(err, ErrLoginRejected) 判定，据此累计失败次数并在
// 达到阈值后自动禁用账号。ret 与 errStr 会附在错误文本里，便于 UI 直接展示
// 服务器给出的真实原因（例如 ret=-2 err="ACCOUNT_NOT_FOUND"）。
var ErrLoginRejected = errors.New("login rejected")

// Client 是 HTTP/2 明文客户端
type Client struct {
	http  *http.Client
	login string
}

// New 创建客户端。loginAddr 形如 "host:port"（默认 DefaultLoginAddr）
func New(loginAddr string) *Client {
	if loginAddr == "" {
		loginAddr = DefaultLoginAddr
	}
	tr := &http2.Transport{
		AllowHTTP: true,
		DialTLSContext: func(ctx context.Context, network, addr string, _ *tls.Config) (net.Conn, error) {
			var d net.Dialer
			return d.DialContext(ctx, network, addr)
		},
	}
	return &Client{
		http:  &http.Client{Transport: tr, Timeout: 30 * time.Second},
		login: loginAddr,
	}
}

// b64 编码（服务器用 Go base64.URLEncoding 解码，见 protocol_notes 踩坑记录）
func b64(b []byte) string {
	return base64.URLEncoding.EncodeToString(b)
}

// post 发送 HTTP/2 POST，返回响应体
func (c *Client) post(ctx context.Context, url string, body []byte) ([]byte, int, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, 0, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "Go-http-client/2.0")
	req.Header.Set("Content-Length", fmt.Sprint(len(body)))

	resp, err := c.http.Do(req)
	if err != nil {
		return nil, 0, err
	}
	defer resp.Body.Close()
	rb, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, resp.StatusCode, err
	}
	return rb, resp.StatusCode, nil
}

// LoginResult 是登录响应的关键字段
type LoginResult struct {
	UID   uint32
	Token string
	Gate  string // "ip:port"（网关 HTTP/2 地址）
}

// Login 完成 HTTP/2 登录：POST /login/，body=base64url([00 01][LsLoginCMsg])
func (c *Client) Login(ctx context.Context, account, pwdHash, mac string, raknetID uint64) (*LoginResult, error) {
	body := append([]byte{0x00, 0x01}, protocol.BuildLoginProto(account, pwdHash, mac, raknetID)...)
	resp, status, err := c.post(ctx, "http://"+c.login+"/login/", []byte(b64(body)))
	if err != nil {
		return nil, fmt.Errorf("login http: %w", err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("login status %d: %s", status, strings.TrimSpace(string(resp)))
	}
	dec, err := base64.URLEncoding.DecodeString(strings.TrimSpace(string(resp)))
	if err != nil {
		return nil, fmt.Errorf("login resp b64: %w body=%q", err, string(resp))
	}
	fields := proto.Parse(dec)
	ret := proto.GetVarint(fields, 1)
	if ret != 1 {
		errStr := proto.GetString(fields, 2)
		// 包装哨兵错误，让上层能区分"服务器拒绝"与"网络故障"。
		// 注意：errStr 是服务器下发的原始原因，会一路透传到 UI 展示。
		return nil, fmt.Errorf("%w ret=%d err=%q", ErrLoginRejected, int64(ret), errStr)
	}
	res := &LoginResult{
		UID:   uint32(proto.GetVarint(fields, 3)),
		Token: proto.GetString(fields, 4),
	}
	// f5 = ServerInfo{httpAddr}
	if g := proto.GetBytes(fields, 5); g != nil {
		gf := proto.Parse(g)
		res.Gate = proto.GetString(gf, 1)
	}
	if res.Token == "" || res.Gate == "" {
		return nil, fmt.Errorf("login resp missing token/gate: %s", dec)
	}
	return res, nil
}

// Game 发送网关业务请求：POST /game/，body=base64url([2B MsgID LE][protobuf])
// gateAddr 形如 "ip:port"；返回解码后的 protobuf 响应体
func (c *Client) Game(ctx context.Context, gateAddr string, msgID uint16, fields []byte) ([]byte, error) {
	return c.raw(ctx, gateAddr, "/game/", msgID, fields)
}

// Zone 发送分区/房间请求：POST /zone/2/，body=base64url([2B MsgID LE][protobuf])
// 实测区/房间消息与业务消息共用同一 gate 地址与 HTTP/2 连接
func (c *Client) Zone(ctx context.Context, gateAddr string, msgID uint16, fields []byte) ([]byte, error) {
	return c.raw(ctx, gateAddr, "/zone/2/", msgID, fields)
}

// raw 发送指定路径的网关请求，返回解码后的 protobuf 响应体
func (c *Client) raw(ctx context.Context, gateAddr, path string, msgID uint16, fields []byte) ([]byte, error) {
	body := protocol.GameBody(msgID, fields)
	resp, status, err := c.post(ctx, "http://"+gateAddr+path, []byte(b64(body)))
	if err != nil {
		return nil, fmt.Errorf("%s(%d) http: %w", path, msgID, err)
	}
	if status != http.StatusOK {
		return nil, fmt.Errorf("%s(%d) status %d: %s", path, msgID, status, strings.TrimSpace(string(resp)))
	}
	// 实测响应 base64 可能为 url-safe 带填充或无填充，两种都尝试
	s := strings.TrimSpace(string(resp))
	// 实测部分响应（如战队详情 596/f3=3）以 0x2B('+') 前缀标记后续为 base64url(zlib) 压缩数据
	if strings.HasPrefix(s, "+") {
		s = s[1:]
	}
	dec, err := base64.URLEncoding.DecodeString(s)
	if err != nil {
		dec, err = base64.RawURLEncoding.DecodeString(s)
	}
	if err != nil {
		return nil, fmt.Errorf("%s(%d) resp b64: %w", path, msgID, err)
	}
	// /zone/2/ 等部分响应经 zlib 压缩（明文 protobuf 则原样保留），统一尝试解压
	dec = tryDecompress(dec)
	slog.Debug("gate resp", "path", path, "msgid", msgID, "len", len(dec))
	return dec, nil
}

// tryDecompress 对 zlib 压缩数据尝试解压；非 zlib 数据原样返回。
func tryDecompress(raw []byte) []byte {
	if len(raw) < 2 || raw[0] != 0x78 {
		return raw
	}
	zr, err := zlib.NewReader(bytes.NewReader(raw))
	if err != nil {
		return raw
	}
	defer zr.Close()
	out, err := io.ReadAll(zr)
	if err != nil {
		return raw
	}
	return out
}
