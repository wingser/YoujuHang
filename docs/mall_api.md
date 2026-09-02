# 游聚商城礼包领取接口分析

## 入口

商城首页：`http://gotvgmall.gotvg.cn/?token=<token>&userid=<uid>`

- token：Gate 登录后返回的 session_key（与游戏 token 相同）
- userid：UID（如 5571561）
- 同时需要 Cookie：`PHPSESSID=<session_id>`

## 领取接口

```
POST http://gotvgmall.gotvg.cn/index.php?m=index&a=ajax_get_package
Content-Type: application/x-www-form-urlencoded; charset=UTF-8
Cookie: PHPSESSID=<session_id>
Referer: http://gotvgmall.gotvg.cn/
X-Requested-With: XMLHttpRequest

id=<package_id>&userid=<uid>&token=<token>
```

## 礼包 ID 映射

| 礼包名称 | ID | 领取时间 |
|---|---|---|
| 微信绑定礼包 | 1 | 每月 1-7 日 |
| VIP 专属礼包 | 4 | 每月 26 日开始 |
| SVIP 专属礼包 | 5 | 每月 26 日开始 |
| SSVIP 专属礼包 | 6 | 每月 26 日开始 |

## 响应格式

```json
// 失败
{"status":0,"info":"不在领取时间范围内"}

// 成功（推测）
{"status":1,"info":"领取成功"}
```

## 注意事项

- 领取需要同时满足：在有效期内 + 用户有对应 VIP 等级
- 微信绑定礼包每月前 7 天可领，过期后 status=0
- VIP/SVIP/SSVIP 礼包每月 26 日开始，需对应会员等级
