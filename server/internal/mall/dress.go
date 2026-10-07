package mall

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"regexp"
	"strconv"
	"strings"
)

// 本文件实现「个人空间装扮（头像）」相关接口。
//
// 协议来源：2026-09-09 实测抓包（captures/avatar_chouyoku_20260909_*.pcapng），
// 详见 docs/avatar_api.md。要点：
//
//  1. 装扮列表：POST / ，表单带 space=1&good_class=dress&page=N，返回 HTML
//     （不是 JSON），每个头像是一段 <div class="good_item">。
//  2. 佩戴：POST /index.php?m=index&a=dressup，表单 good_id/userid/token，
//     返回 {"num":2,"data":"装备成功","good_class":"dress"}。num=2 才是成功。
//  3. 两个接口都依赖 PHPSESSID cookie，必须先 GET 商城首页获取（见 sessionCookie）。

// DressUpPath 佩戴（装备）接口
const DressUpPath = "/index.php?m=index&a=dressup"

// FitConditionPath 佩戴条件检查接口。
// 部分加成型头像绑定页游角色（如"刀剑笑之霸刀 7000 级"），
// 未达标时佩戴会失败，可先用本接口预判。
const FitConditionPath = "/index.php?m=index&a=check_user_fit_condition"

// LeftDaysForever 表示"永久"的剩余天数。
// 页面对永久头像显示「永久」而非「剩余N天」，这里用一个大数表示，
// 便于统一按数值排序比较。
const LeftDaysForever = 9999

// maxDressPages 装扮列表最多翻页数，防止异常情况下无限翻页
const maxDressPages = 20

// DressItem 装扮列表中的一个条目（头像）
type DressItem struct {
	GoodID   int     // 商品 ID，佩戴时用
	Title    string  // 名称，如「铁血士兵」
	Exp      float64 // 经验加成倍数（additional_exp）；<=1 表示无加成
	LeftDays int     // 剩余天数（取自页面「剩余N天」文案）；LeftDaysForever 表示永久
	GameName string  // 绑定的页游名（空表示无绑定条件）
	Level    int     // 绑定的角色等级要求（0 表示无要求）
	URL      string  // 头像图片路径（Public/Mall/...），与 space 页 userAvatar 匹配用
}

// SpacePage 个人空间「我的装扮」页面（GET /?token=..&userid=..&space=1）的解析结果。
//
// 【2026-09-09 重大修正】此前用 POST / 翻页查列表、用内存记录判断"当前佩戴"，
// 两个都是错的：真实客户端打开个人空间时的这个 GET 响应里**同时包含**
// 当前佩戴头像（id="userAvatar" / default_userAvatar / user_try 三处同值）
// 与已拥有的装扮列表（good_item）——一个请求即可获得全部所需数据，
// POST / 只是页面翻页/试穿用的表单请求（语义依赖 user_try，传空会得到异常响应，
// 实测三账号并发时 chouyoku 单独返回了无法解析的内容）。
type SpacePage struct {
	WornURL string      // 当前佩戴头像的图片路径（/Public/Mall/...，含前导斜杠）
	Items   []DressItem // 已拥有的装扮列表（页面第一页）
}

// MatchWorn 在已拥有列表中找到「当前佩戴」的条目。
// URL 匹配做了斜杠归一（userAvatar 带 / 前缀，good_item 的 url 字段没有）。
// 返回 nil 表示当前未佩戴列表中的任何头像（如默认头像 moren.png）。
func (p *SpacePage) MatchWorn() *DressItem {
	return FindByURL(p.Items, p.WornURL)
}

// FindByURL 按 URL（忽略前导斜杠差异）在列表中查找条目。
func FindByURL(items []DressItem, wornURL string) *DressItem {
	if wornURL == "" {
		return nil
	}
	want := strings.Trim(wornURL, "/")
	for i := range items {
		if strings.Trim(items[i].URL, "/") == want {
			return &items[i]
		}
	}
	return nil
}

// DressResult 佩戴接口响应：{"num":2,"data":"装备成功","good_class":"dress"}
type DressResult struct {
	Num       int    `json:"num"`
	Data      string `json:"data"`
	GoodClass string `json:"good_class"`
	Status    int    `json:"status"` // 仅 check_user_fit_condition 返回
	Msg       string `json:"msg"`    // 仅 check_user_fit_condition 返回
}

// Success 佩戴是否成功。
// 前端 JS 判据：num==2 为装备成功；num==1 为有提示消息（通常是不满足佩戴条件）。
func (r *DressResult) Success() bool { return r != nil && r.Num == 2 }

// 列表 HTML 的解析正则。
//
// 注意：这些正则针对服务端实际输出的 HTML 编写（属性顺序、空白与页面一致）。
// 服务端若调整模板，需同步本文件，并补 parseDressItems 的单测。
var (
	// reItemStart 每个头像条目的起点，用于按位置切块（Go 正则不支持 lookahead）
	reItemStart = regexp.MustCompile(`<div class="good_item">`)
	reGoodID    = regexp.MustCompile(`good_id"[^>]*>\s*(\d+)`)
	reTitle     = regexp.MustCompile(`class="title">\s*([^<]+)`)
	reExp       = regexp.MustCompile(`additional_exp"[^>]*>\s*([\d.]+)`)
	// reLeftDays 页面对有时限的头像显示「剩余N天」
	reLeftDays = regexp.MustCompile(`<span>剩余\s*(\d+)\s*天</span>`)
	// reForever 对永久头像显示「永久」
	reForever = regexp.MustCompile(`<span>永久</span>`)
	reGameNam = regexp.MustCompile(`game_name"[^>]*>\s*([^<]*)`)
	reLevel   = regexp.MustCompile(`class="level"[^>]*>\s*(\d+)`)
	reURL     = regexp.MustCompile(`class="url"[^>]*>\s*([^<]+)`)
	// space 页"当前佩戴"：img#userAvatar / div#default_userAvatar / input.user_try 三处同值，
	// 取 default_userAvatar（div 纯文本内容，服务端渲染的权威值，不受页面试穿 JS 影响）
	reWornDefault = regexp.MustCompile(`id="default_userAvatar"[^>]*>\s*([^<\s]+)`)
	reWornImgSrc  = regexp.MustCompile(`<img src="([^"]+)"[^>]+id="userAvatar"`)
)

// GetSpace 拉取个人空间「我的装扮」页面（GET /?token=..&userid=..&space=1）。
//
// 这是查询「当前佩戴」与「已拥有装扮」的正确入口（一个请求全拿到，2026-09-09 抓包确认）：
// 页面通过 URL 参数认证，无需预取 PHPSESSID（首次 GET 即返回完整数据）。
// 列表只含第一页；需要全量翻页时走 ListDress。
func (c *Client) GetSpace(ctx context.Context, uid uint32, token string) (*SpacePage, error) {
	target := fmt.Sprintf("%s/?token=%s&userid=%d&space=1", BaseURL, url.QueryEscape(token), uid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, target, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mall space: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("mall space: HTTP %d", resp.StatusCode)
	}
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	return parseSpacePage(string(body)), nil
}

// parseSpacePage 解析 space 页 HTML（独立出来便于单测）。
func parseSpacePage(html string) *SpacePage {
	page := &SpacePage{Items: parseDressItems(html)}
	// 当前佩戴：default_userAvatar 为权威值（服务端渲染），img#userAvatar 作兜底
	if m := reWornDefault.FindStringSubmatch(html); m != nil {
		page.WornURL = strings.TrimSpace(m[1])
	} else if m := reWornImgSrc.FindStringSubmatch(html); m != nil {
		page.WornURL = strings.TrimSpace(m[1])
	}
	return page
}

// ListDress 拉取「头像」分类的装扮列表，自动翻页直到没有新条目。
// wornURL 为当前佩戴头像的图片路径（可为空），随表单 user_try 提交——
// 与真实客户端行为一致，服务端语义依赖它。
//
// 返回的条目已按 GoodID 去重（翻页可能重叠），顺序为翻页顺序。
func (c *Client) ListDress(ctx context.Context, uid uint32, token string, wornURL string) ([]DressItem, error) {
	cookies, err := c.sessionCookie(ctx, uid, token)
	if err != nil {
		return nil, err
	}
	var all []DressItem
	seen := make(map[int]bool)
	for page := 1; page <= maxDressPages; page++ {
		body, err := c.dressPage(ctx, uid, token, page, wornURL, cookies)
		if err != nil {
			if page == 1 {
				return nil, err
			}
			// 后续页失败不致命：用已拿到的条目继续（列表页偶发抖动）
			break
		}
		items := parseDressItems(body)
		fresh := 0
		for _, it := range items {
			if !seen[it.GoodID] {
				seen[it.GoodID] = true
				all = append(all, it)
				fresh++
			}
		}
		// 本页无新条目 → 已翻完（或服务端对超范围页码重复返回最后一页）
		if fresh == 0 {
			break
		}
	}
	return all, nil
}

// DressUp 佩戴指定装扮。
func (c *Client) DressUp(ctx context.Context, uid uint32, token string, goodID int) (*DressResult, error) {
	cookies, err := c.sessionCookie(ctx, uid, token)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("good_id", strconv.Itoa(goodID))
	form.Set("userid", fmt.Sprint(uid))
	form.Set("token", token)
	return c.postDressResult(ctx, BaseURL+DressUpPath, uid, token, form, cookies)
}

// CheckFit 检查是否满足某装扮的佩戴条件。
//
// 返回 Status==2 时表示不满足（Msg 形如「在页游《X》中没有角色…」）。
// 佩戴前预判可避免无意义的失败请求；该接口非必须，失败不应阻断佩戴流程。
func (c *Client) CheckFit(ctx context.Context, uid uint32, token string, goodID int) (*DressResult, error) {
	cookies, err := c.sessionCookie(ctx, uid, token)
	if err != nil {
		return nil, err
	}
	form := url.Values{}
	form.Set("good_id", strconv.Itoa(goodID))
	form.Set("userid", fmt.Sprint(uid))
	form.Set("token", token)
	return c.postDressResult(ctx, BaseURL+FitConditionPath, uid, token, form, cookies)
}

// sessionCookie GET 商城首页拿 PHPSESSID。
// 商城接口依赖该 cookie，直接 POST 会因无会话被拒。
func (c *Client) sessionCookie(ctx context.Context, uid uint32, token string) ([]*http.Cookie, error) {
	home := fmt.Sprintf("%s/?token=%s&userid=%d", BaseURL, url.QueryEscape(token), uid)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, home, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", userAgent)
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("mall home: %w", err)
	}
	defer resp.Body.Close()
	_, _ = io.Copy(io.Discard, resp.Body)
	return resp.Cookies(), nil
}

// dressPage 拉取指定页的装扮列表 HTML
func (c *Client) dressPage(ctx context.Context, uid uint32, token string, page int, wornURL string, cookies []*http.Cookie) (string, error) {
	form := url.Values{}
	form.Set("space", "1")
	form.Set("userid", fmt.Sprint(uid))
	form.Set("token", token)
	form.Set("page", strconv.Itoa(page))
	form.Set("good_class", "dress")
	// user_try/userbg_try 是页面"试穿状态"（真实客户端始终提交当前佩戴值），
	// 服务端语义依赖它们——2026-09-09 实测传空在部分会话下会返回无法解析的内容。
	form.Set("userbg_try", "")
	form.Set("user_try", wornURL)
	form.Set("prop_type", "")
	form.Set("page_up_hid", "")
	form.Set("page_down_hid", "1")
	form.Set("real_type", "")
	form.Set("item_type", "")

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, BaseURL+"/", strings.NewReader(form.Encode()))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("User-Agent", userAgent)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("mall dress list page=%d: %w", page, err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", err
	}
	return string(body), nil
}

// postDressResult 通用的装扮类 POST（佩戴 / 条件检查）
func (c *Client) postDressResult(ctx context.Context, target string, uid uint32, token string,
	form url.Values, cookies []*http.Cookie) (*DressResult, error) {
	home := fmt.Sprintf("%s/?token=%s&userid=%d", BaseURL, url.QueryEscape(token), uid)
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, target, strings.NewReader(form.Encode()))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded; charset=UTF-8")
	req.Header.Set("Referer", home)
	req.Header.Set("X-Requested-With", "XMLHttpRequest")
	req.Header.Set("User-Agent", userAgent)
	for _, ck := range cookies {
		req.AddCookie(ck)
	}
	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	var r DressResult
	if err := json.Unmarshal(body, &r); err != nil {
		return nil, fmt.Errorf("mall resp %q: %w", strings.TrimSpace(string(body)), err)
	}
	return &r, nil
}

// parseDressItems 解析装扮列表 HTML。
//
// 条目结构（服务端模板，字段顺序固定）：
//
//	<div class="good_item">
//	  <div class="title">铁血士兵</div>
//	  <div class="good_id" style="display:none">58</div>
//	  ... <div class="additional_exp" ...>1.4</div> ...
//	  <div class="cont"> ... <span>剩余24天</span> ... </div>   ← 剩余天数在条目末尾
//	</div>
//
// 注意「剩余N天」位于条目内部末尾的 cont 块中，因此按 good_item 起点切块后
// 即可保证字段归属正确；不可单独按 good_id 切块，否则会串到下一个条目。
func parseDressItems(html string) []DressItem {
	locs := reItemStart.FindAllStringIndex(html, -1)
	if len(locs) == 0 {
		return nil
	}
	items := make([]DressItem, 0, len(locs))
	for i, loc := range locs {
		start := loc[0]
		end := len(html)
		if i+1 < len(locs) {
			end = locs[i+1][0]
		}
		block := html[start:end]

		m := reGoodID.FindStringSubmatch(block)
		if m == nil {
			continue
		}
		goodID, err := strconv.Atoi(m[1])
		if err != nil {
			continue
		}
		it := DressItem{GoodID: goodID}
		if m := reTitle.FindStringSubmatch(block); m != nil {
			it.Title = strings.TrimSpace(m[1])
		}
		if m := reExp.FindStringSubmatch(block); m != nil {
			if v, err := strconv.ParseFloat(m[1], 64); err == nil {
				it.Exp = v
			}
		}
		if m := reGameNam.FindStringSubmatch(block); m != nil {
			it.GameName = strings.TrimSpace(m[1])
		}
		if m := reLevel.FindStringSubmatch(block); m != nil {
			it.Level, _ = strconv.Atoi(m[1])
		}
		if m := reURL.FindStringSubmatch(block); m != nil {
			it.URL = strings.TrimSpace(m[1])
		}
		// 剩余天数：优先「剩余N天」，其次「永久」，都没有则视为未知(0)
		if m := reLeftDays.FindStringSubmatch(block); m != nil {
			it.LeftDays, _ = strconv.Atoi(m[1])
		} else if reForever.MatchString(block) {
			it.LeftDays = LeftDaysForever
		}
		items = append(items, it)
	}
	return items
}

// MergeDressItems 合并两个来源的装扮条目，按 GoodID 去重。
//
// 为什么需要（2026-10-07 事故修复）：装扮信息有两个不同来源，内容互补——
//
//	space 页（GetSpace）  → **我拥有的装扮**，带真实「剩余N天/永久」，
//	                        但只含首页若干条；
//	列表页（ListDress）   → **可购买的商品目录**，条目多，
//	                        但未拥有的商品没有有效期（LeftDays 恒为 0）。
//
// 此前选候选时只用列表页，导致「明明拥有 1.4 倍剩 21 天的头像（good_id=127）
// 却报告无可用加成头像」。合并时若同一 GoodID 两边都有，优先保留 primary
// （space 页）的数据，并用 extra 补全 primary 缺失的剩余天数。
func MergeDressItems(primary, extra []DressItem) []DressItem {
	out := make([]DressItem, 0, len(primary)+len(extra))
	idx := make(map[int]int, len(primary)+len(extra))
	for _, it := range primary {
		if it.GoodID <= 0 {
			continue
		}
		idx[it.GoodID] = len(out)
		out = append(out, it)
	}
	for _, it := range extra {
		if it.GoodID <= 0 {
			continue
		}
		if i, ok := idx[it.GoodID]; ok {
			// 已存在：仅在 primary 没拿到有效期时用 extra 补全
			if out[i].LeftDays == 0 && it.LeftDays != 0 {
				out[i].LeftDays = it.LeftDays
			}
			continue
		}
		idx[it.GoodID] = len(out)
		out = append(out, it)
	}
	return out
}

// PickBestDress 从列表中挑选最值得佩戴的头像。
//
// 策略（经验加成最大化）：
//  1. 只看有加成的（Exp > 1）；无加成头像不参与，避免把加成头像换掉。
//  2. 跳过已过期的（LeftDays <= 0）。
//  3. 加成高的优先；加成相同时，剩余天数多的优先（减少切换频率）。
//
// 返回 nil 表示没有可用的加成头像。
func PickBestDress(items []DressItem) *DressItem {
	var best *DressItem
	for i := range items {
		it := &items[i]
		if it.Exp <= 1 || it.LeftDays <= 0 {
			continue
		}
		if best == nil || it.Exp > best.Exp ||
			(it.Exp == best.Exp && it.LeftDays > best.LeftDays) {
			best = it
		}
	}
	return best
}
