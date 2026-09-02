// Package mall 实现商城礼包领取（gotvgmall.gotvg.cn）
// 接口实测见 docs/mall_api.md（2026-08-26 抓包确认）
package mall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// BaseURL 商城地址（明文 HTTP，端口 80）
	BaseURL = "http://gotvgmall.gotvg.cn"
	// ClaimPath 领取接口
	ClaimPath = "/index.php?m=index&a=ajax_get_package"
	// WechatPackageID 微信绑定礼包 ID（每月 1-7 日可领）
	WechatPackageID = 1
)

// Result 是领取接口的 JSON 响应
// 成功：{"status":1,...}；失败：{"status":0,"info":"不在领取时间范围内"}
type Result struct {
	Status int    `json:"status"`
	Info   string `json:"info"`
}

// Client 是商城 HTTP 客户端
type Client struct {
	hc *http.Client
}

// New 创建商城客户端
func New() *Client {
	return &Client{
		hc: &http.Client{Timeout: 30 * time.Second},
	}
}

// Claim 领取指定礼包。uid/token 为游戏登录会话凭据。
// 流程：先 GET 商城首页拿 PHPSESSID cookie，再 POST 领取接口。
func (c *Client) Claim(ctx context.Context, uid uint32, token string, pkgID int) (*Result, error) {
	home := fmt.Sprintf("%s/?token=%s&userid=%d", BaseURL, url.QueryEscape(token), uid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, home, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "Mozilla/4.0 (compatible; MSIE 7.0; Windows NT 6.2; WOW64; Trident/7.0)")
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mall home: %w", err)
	}
	_, _ = io.Copy(io.Discard, resp.Body)
	resp.Body.Close()
	cookies := resp.Cookies()

	form := url.Values{}
	form.Set("id", fmt.Sprint(pkgID))
	form.Set("userid", fmt.Sprint(uid))
	form.Set("token", token)

	req2, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+ClaimPath, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req2.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req2.Header.Set("Referer", home)
	req2.Header.Set("X-Requested-With", "XMLHttpRequest")
	req2.Header.Set("User-Agent", "Mozilla/4.0 (compatible; MSIE 7.0; Windows NT 6.2; WOW64; Trident/7.0)")
	for _, ck := range cookies {
		req2.AddCookie(ck)
	}

	resp2, err := c.hc.Do(req2)
	if err != nil {
		return nil, fmt.Errorf("mall claim: %w", err)
	}
	defer resp2.Body.Close()
	body, err := io.ReadAll(resp2.Body)
	if err != nil {
		return nil, err
	}
	var r Result
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("mall resp %q: %w", strings.TrimSpace(string(body)), err)
	}
	return &r, nil
}
