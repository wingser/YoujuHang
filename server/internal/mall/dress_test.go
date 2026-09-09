package mall

import "testing"

// dressListHTML 装扮列表 HTML 样本。
//
// 取自 2026-09-09 真实抓包（captures/avatar_chouyoku_20260909_*.pcapng），
// 字段顺序/属性与服务端模板一致，仅裁掉无关图片字段。
//
// 这三个条目是回归测试的关键，覆盖三种典型情况：
//   - good_id=6  赵云      加成 1.1  永久
//   - good_id=58 铁血士兵  加成 1.4  剩余 24 天  ← 实际佩戴中的
//   - good_id=125 女车手   加成 1.4  剩余 9 天
//
// 特别注意「剩余N天」位于条目内部末尾的 cont 块中。
// 早期版本按 good_id 位置切块，导致 58 的剩余天数被串成下一个条目的 9 天、
// 「剩余24天」被误读成「剩余4天」（该 bug 已修，此用例用于防止回归）。
const dressListHTML = `
<div class="good_item">
    <div class="title">赵云</div>
    <div class="good_id" style="display:none">6</div>
    <div class="price" style="display:none">0</div>
    <div class="game_name" style="display:none">游聚平台</div>
    <div class="gid" style="display:none">27</div>
    <div class="server_name" style="display:none">任意服</div>
    <div class="level_type" style="display:none">游聚平台等级</div>
    <div class="level" style="display:none">60</div>
    <div class="additional_exp" style="display:none">1.1</div>
    <div class="url" style="display:none">Public/Mall/20240829/20240829261430.jpg</div>
    <div class="end_time" style="display:none">2026-09-02 23:59:00</div>
    <div class="cont">
        <a href="#" class="setAvaterBtn">
            <img src="/Public/Mall/20240829/20240829261430.jpg"/>
        </a>
        <div class="control">
            <div class="price"><em></em><span>永久</span></div>
            <div class="btn_lst"><a href="#" class="dressup">装备</a><a href="#" class="xufei">续费</a></div>
        </div>
    </div>
</div>
<div class="good_item">
    <div class="title">铁血士兵</div>
    <div class="good_id" style="display:none">58</div>
    <div class="price" style="display:none">0</div>
    <div class="game_name" style="display:none">维京传奇</div>
    <div class="gid" style="display:none">99</div>
    <div class="server_name" style="display:none">144服</div>
    <div class="level_type" style="display:none">游戏角色等级</div>
    <div class="level" style="display:none">420</div>
    <div class="additional_exp" style="display:none">1.4</div>
    <div class="url" style="display:none">Public/Mall/20250314/20250314453060.jpg</div>
    <div class="end_time" style="display:none">2026-09-02 23:59:00</div>
    <div class="cont">
        <a href="#" class="setAvaterBtn">
            <img src="/Public/Mall/20250314/20250314453060.jpg"/>
        </a>
        <div class="control">
            <div class="price"><em></em><span>剩余24天</span></div>
            <div class="btn_lst"><a href="#" class="dressup">装备</a><a href="#" class="xufei">续费</a></div>
        </div>
    </div>
</div>
<div class="good_item">
    <div class="title">女车手</div>
    <div class="good_id" style="display:none">125</div>
    <div class="price" style="display:none">0</div>
    <div class="game_name" style="display:none">霸者天下</div>
    <div class="gid" style="display:none">118</div>
    <div class="server_name" style="display:none">37服</div>
    <div class="level_type" style="display:none">游戏角色等级</div>
    <div class="level" style="display:none">260</div>
    <div class="additional_exp" style="display:none">1.4</div>
    <div class="url" style="display:none">Public/Mall/20250211/20250211498396.jpg</div>
    <div class="end_time" style="display:none">2026-08-18 23:59:00</div>
    <div class="cont">
        <a href="#" class="setAvaterBtn">
            <img src="/Public/Mall/20250211/20250211498396.jpg"/>
        </a>
        <div class="control">
            <div class="price"><em></em><span>剩余9天</span></div>
            <div class="btn_lst"><a href="#" class="dressup">装备</a><a href="#" class="xufei">续费</a></div>
        </div>
    </div>
</div>
`

// TestParseDressItems 验证列表解析，重点是字段不串到相邻条目。
func TestParseDressItems(t *testing.T) {
	items := parseDressItems(dressListHTML)
	if len(items) != 3 {
		t.Fatalf("应解析出 3 个条目，实际 %d：%+v", len(items), items)
	}

	byID := make(map[int]DressItem, len(items))
	for _, it := range items {
		byID[it.GoodID] = it
	}

	// good_id=6：永久，加成 1.1
	if it := byID[6]; it.LeftDays != LeftDaysForever {
		t.Errorf("good_id=6 应为永久(%d)，实际 %d", LeftDaysForever, it.LeftDays)
	} else if it.Exp != 1.1 {
		t.Errorf("good_id=6 加成应为 1.1，实际 %v", it.Exp)
	}

	// good_id=58：核心回归点——剩余 24 天（早期 bug 会读成 4 天 / 串成 9 天）
	it58, ok := byID[58]
	if !ok {
		t.Fatalf("未解析出 good_id=58")
	}
	if it58.LeftDays != 24 {
		t.Errorf("good_id=58 剩余应为 24 天，实际 %d（可能被串到相邻条目）", it58.LeftDays)
	}
	if it58.Exp != 1.4 {
		t.Errorf("good_id=58 加成应为 1.4，实际 %v", it58.Exp)
	}
	if it58.Title != "铁血士兵" {
		t.Errorf("good_id=58 名称应为 铁血士兵，实际 %q", it58.Title)
	}
	if it58.GameName != "维京传奇" || it58.Level != 420 {
		t.Errorf("good_id=58 绑定应为 维京传奇/420级，实际 %q/%d", it58.GameName, it58.Level)
	}

	// good_id=125：剩余 9 天
	if it := byID[125]; it.LeftDays != 9 {
		t.Errorf("good_id=125 剩余应为 9 天，实际 %d", it.LeftDays)
	} else if it.Exp != 1.4 {
		t.Errorf("good_id=125 加成应为 1.4，实际 %v", it.Exp)
	}
}

// TestPickBestDress 选择策略：加成最高优先，同加成时剩余更久的优先。
func TestPickBestDress(t *testing.T) {
	items := parseDressItems(dressListHTML)
	best := PickBestDress(items)
	if best == nil {
		t.Fatal("应选出头像，实际为 nil")
	}
	// 58 与 125 同为 1.4，但 58 剩余 24 天 > 125 的 9 天
	if best.GoodID != 58 {
		t.Errorf("应选 good_id=58（1.4 且剩余 24 天），实际 %d", best.GoodID)
	}

	// 无加成头像不参与：只剩 1.1 时也应选出（1.1 > 1）
	only := []DressItem{{GoodID: 6, Exp: 1.1, LeftDays: LeftDaysForever}}
	if got := PickBestDress(only); got == nil || got.GoodID != 6 {
		t.Errorf("仅有无加成->低加成时也应选出，实际 %+v", got)
	}

	// 全部无加成 → 不选（避免把加成头像换掉）
	if got := PickBestDress([]DressItem{{GoodID: 1, Exp: 1, LeftDays: 10}}); got != nil {
		t.Errorf("无加成头像不应被选中，实际 %+v", got)
	}

	// 已过期（剩余 0 天）不选
	if got := PickBestDress([]DressItem{{GoodID: 2, Exp: 1.4, LeftDays: 0}}); got != nil {
		t.Errorf("已过期头像不应被选中，实际 %+v", got)
	}

	// 空列表
	if got := PickBestDress(nil); got != nil {
		t.Errorf("空列表应返回 nil，实际 %+v", got)
	}
}

// spacePageHTML 取自 2026-09-09 真实抓包（chouyoku 的 GET /?...&space=1 响应），
// 仅保留关键字段：当前佩戴（userAvatar / default_userAvatar / user_try 三处同值）
// 与已拥有列表中的两个条目。
// 这是「查询当前佩戴」的正确数据源（2026-09-09 修正：此前错误地用 POST 翻页 +
// 内存记录判断佩戴，导致 chouyoku 查询异常且重启会重复佩戴）。
const spacePageHTML = `
    <img src="/Public/Mall/20250314/20250314453060.jpg" id="userAvatar" />
    <div id="default_userAvatar" style="display:none">/Public/Mall/20250314/20250314453060.jpg</div>
    <input class="user_try" name="user_try" type="hidden" value="/Public/Mall/20250314/20250314453060.jpg" />
    <div class="avatar_list">
    <div class="good_item">
        <div class="title">铁血士兵</div>
        <div class="good_id" style="display:none">58</div>
        <div class="game_name" style="display:none">维京传奇</div>
        <div class="level" style="display:none">420</div>
        <div class="additional_exp" style="display:none">1.4</div>
        <div class="url" style="display:none">Public/Mall/20250314/20250314453060.jpg</div>
        <div class="end_time" style="display:none">2026-09-02 23:59:00</div>
        <div class="cont">
            <a href="#" class="setAvaterBtn"><img src="/Public/Mall/20250314/20250314453060.jpg"/></a>
            <div class="control">
                <div class="price"><em></em><span>剩余24天</span></div>
            </div>
        </div>
    </div>
    <div class="good_item">
        <div class="title">女车手</div>
        <div class="good_id" style="display:none">125</div>
        <div class="additional_exp" style="display:none">1.4</div>
        <div class="url" style="display:none">Public/Mall/20250211/20250211498396.jpg</div>
        <div class="cont">
            <div class="control">
                <div class="price"><em></em><span>剩余9天</span></div>
            </div>
        </div>
    </div>
    </div>
`

// TestParseSpacePage space 页解析：当前佩戴提取 + 与拥有列表按 URL 匹配。
func TestParseSpacePage(t *testing.T) {
	page := parseSpacePage(spacePageHTML)
	if page.WornURL != "/Public/Mall/20250314/20250314453060.jpg" {
		t.Fatalf("WornURL 应为 /Public/Mall/20250314/20250314453060.jpg，实际 %q", page.WornURL)
	}
	if len(page.Items) != 2 {
		t.Fatalf("应解析出 2 个条目，实际 %d", len(page.Items))
	}
	worn := page.MatchWorn()
	if worn == nil {
		t.Fatal("当前佩戴（58 铁血士兵）应能在拥有列表中匹配到（注意 userAvatar 带前导斜杠、条目 url 不带的差异）")
	}
	if worn.GoodID != 58 || worn.Exp != 1.4 || worn.LeftDays != 24 {
		t.Errorf("佩戴条目应为 58/1.4/剩24天，实际 %+v", *worn)
	}

	// 未佩戴任何加成头像（默认头像 moren.png）：匹配不到 → 业务层走"需要更换"
	empty := &SpacePage{WornURL: "/Tpl/Template_mall/images/moren.png", Items: page.Items}
	if empty.MatchWorn() != nil {
		t.Error("默认头像不应匹配到任何条目")
	}
}
