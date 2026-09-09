# 头像装扮（经验加成）接口

> 抓包时间：2026-09-09　样本：`captures/avatar_chouyoku_20260909_00001_20260909120210.pcapng`
> 实现：`server/internal/mall/dress.go`（协议）、`server/internal/biz/avatar.go`（业务）
> 用途：自动佩戴「平台经验加成」最高的头像，到期自动换戴。

## 一、背景

个人空间的部分头像带**平台经验加成**（`additional_exp`，实测有 1.1 / 1.2 / 1.4 等档位），
但都是**限时**的（页面显示「剩余N天」，`buy_msg` 为「30天 - 0游币」）。到期加成即失效。
多账号手动维护成本高，故由程序每天检查并自动换戴。

## 二、接口

### 0. 个人空间初始化页（★ 查询当前佩戴的正确入口，2026-09-09 修正）

```
GET http://gotvgmall.gotvg.cn/?token=<token>&userid=<uid>&space=1
```

真实客户端打开个人空间时的第一个请求。响应 HTML **同时包含**：

```html
<!-- 当前佩戴头像（三处同值）-->
<img src="/Public/Mall/20250314/20250314453060.jpg" id="userAvatar" />
<div id="default_userAvatar" style="display:none">/Public/Mall/20250314/20250314453060.jpg</div>
<input class="user_try" name="user_try" type="hidden" value="..." />
<!-- 已拥有的装扮列表（good_item，结构与下文列表接口一致）-->
```

- 通过 URL 参数认证，**无需预取 PHPSESSID**（抓包确认首次 GET 即返回完整数据）
- 这就是"查询当前佩戴"的唯一正确来源——列表接口的 HTML 里**没有**「已装备」标记
- 实测（chouyoku LV179）：佩戴中头像的图片正好等于 `userAvatar`，列表里能按 URL 匹配到该条目

> **2026-09-09 教训**：最初实现用 `POST /` 翻页查列表 + 程序内存记录判断"当前佩戴"，
> 两个都错——`POST /` 是页面翻页/试穿的表单请求，语义依赖 `user_try`（当前佩戴值），
> 传空在部分会话下返回无法解析的内容（三账号并发时 chouyoku 单独命中）；
> 内存判断则导致每次重启都重复佩戴。用户指出"个人空间初始化页面里已经显示了
> 正确的头像"后，从旧 pcap 中挖出 space 页响应证实。

### 1. 装扮列表（翻页，仅在需要更换时使用）

```
POST http://gotvgmall.gotvg.cn/
Content-Type: application/x-www-form-urlencoded

space=1&userid=<uid>&token=<token>&page=<N>&good_class=dress
&userbg_try=&user_try=<当前佩戴图片>&prop_type=&page_up_hid=&page_down_hid=1&real_type=&item_type=
```

- `good_class=dress` = 头像（另有背景/道具/称号等分类）
- `user_try` 必须传当前佩戴头像路径（与真实客户端一致），传空会得到异常响应
- `page` 从 1 开始翻页；返回 **HTML**（不是 JSON）
- 翻页终止条件：本页解析不出新 `good_id`（服务端对超范围页码会重复返回最后一页）
- 依赖 `PHPSESSID` cookie —— 需先 `GET /?token=..&userid=..` 获取

### 2. 佩戴

```
POST http://gotvgmall.gotvg.cn/index.php?m=index&a=dressup
good_id=<商品ID>&userid=<uid>&token=<token>

→ {"num":2,"data":"装备成功","good_class":"dress"}
```

- **`num==2` 才是成功**（前端 JS 判据）；`num==1` 是带提示消息的失败。
- 响应里的 `data` 是中文提示，可直接展示给用户。

### 3. 佩戴条件检查（可选）

```
POST http://gotvgmall.gotvg.cn/index.php?m=index&a=check_user_fit_condition
good_id=<商品ID>&userid=<uid>&token=<token>

→ {"status":2,"msg":"在页游《龙域世界》中没有角色,是否去游戏创建？","url":"..."}
```

部分加成型头像**绑定页游角色**，未创建角色/等级不足时佩戴会失败。
可在佩戴前用本接口预判（`status==2` 表示不满足）。当前实现是直接佩戴、失败时记录原因。

## 三、列表 HTML 结构

每个头像是一段 `<div class="good_item">`（字段顺序与服务端模板一致）：

```html
<div class="good_item">
    <div class="title">铁血士兵</div>                        <!-- 名称 -->
    <div class="good_id" style="display:none">58</div>       <!-- 佩戴用的商品 ID -->
    <div class="price" style="display:none">0</div>
    <div class="game_name" style="display:none">维京传奇</div> <!-- 绑定的页游 -->
    <div class="gid" style="display:none">99</div>
    <div class="server_name" style="display:none">144服</div>
    <div class="level_type" style="display:none">游戏角色等级</div>
    <div class="level" style="display:none">420</div>        <!-- 等级要求 -->
    <div class="additional_exp" style="display:none">1.4</div><!-- ★ 经验加成倍数 -->
    <div class="explain" style="display:none"></div>
    <div class="buy_msg" style="display:none">30天 - 0游币</div>
    <div class="url" style="display:none">Public/Mall/20250314/20250314453060.jpg</div>
    <div class="end_time" style="display:none">2026-09-02 23:59:00</div>
    <div class="cont">                                        <!-- 展示块，在条目内部末尾 -->
        <a href="#" class="setAvaterBtn"><img src="..."/></a>
        <div class="control">
            <div class="price"><em></em><span>剩余24天</span></div>  <!-- ★ 剩余天数 -->
            <div class="btn_lst"><a href="#" class="dressup">装备</a> …</div>
        </div>
    </div>
</div>
```

## 四、两个必须记住的坑

### 坑 1：`end_time` **不是**当前到期时间

实测（今天 2026-09-09）：

| good_id | `end_time` 字段 | 页面「剩余N天」 | 差值 |
|---|---|---|---|
| 58  | 2026-09-02 | 剩余 **24** 天 | ≈30 天 |
| 125 | 2026-08-18 | 剩余 **9** 天  | ≈30 天 |

两者都差约 30 天，而 `buy_msg` 正是「30天 - 0游币」。
即 **`end_time` 是上一次购买/续费的周期起点（或上次到期日），真实到期 ≈ `end_time` + 30 天**。

> **结论：判断有没有过期必须读页面「剩余N天」文案，不能用 `end_time`。**
> 用 `end_time` 会把还剩 24 天的头像误判为「已过期」，进而反复切换。

### 坑 2：「剩余N天」在条目**内部末尾**的 `cont` 块里

它不在 `good_item` 的开头，而是在条目末尾的展示块中。
因此**必须按 `<div class="good_item">` 起点切块**再解析；
若按 `good_id` 位置切块，剩余天数会串到下一个条目
（58 的 24 天会被读成 125 的 9 天 —— 该 bug 已修，见 `dress_test.go` 的回归用例）。

### 附：解析抓包时的工具坑

用 `tshark -z follow,http,hex,N` 导出时，每行格式为
`8字节hex + 2空格 + 8字节hex`，hex 区共 **48** 字符。
按 47 截取会**每 16 字节丢 1 个字符**，导致「剩余24天」显示成「剩余4天」、
名称「铁血士兵」变成「浴血士兵」、日期 `2026-09-02` 变成 `2026-09-0`。
这类"看起来合理但数字不对"的结果极易误导分析结论。

## 五、程序行为（2026-09-09 按用户需求重写）

- 配置项 `avatar_enabled`（默认 `true`），每天检查一次（`avatarKey` 按日期去重，跨天重置）。
- **决策以服务端查询为唯一事实**（`mall.GetSpace`，一个 GET 请求）：
  - 当前佩戴的加成头像仍有效（`Exp>1` 且未过期）→ **什么都不做**，绝不重复佩戴；
    因此重启、重登都不会触发佩戴动作（旧实现用内存记录判断，重启即重戴，已废）。
  - 未佩戴 / 佩戴无加成 / 已过期 → 才翻页拉全量列表，`PickBestDress` 选最优执行佩戴。
- 选择策略（`mall.PickBestDress`）：只看加成 > 1 且未过期的；
  加成高的优先，加成相同则剩余天数多的优先（减少切换频率）。
- 常态下每天每账号只有 **1 个 GET 请求**（space 页）；只有需要更换时才翻页。
- 佩戴失败（绑定条件不满足等）只记日志与 UI 状态，当天不重试。

## 六、待验证

加成文案是「**平台经验**加成」，理论上挂机涨的就是平台经验，
但**尚未实测确认加成是否作用于挂机获得的经验**。
建议对比「佩戴 1.4 倍头像」与「不佩戴」在相同时长内 1042 上报的经验增量来验证。
