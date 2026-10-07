# -*- coding: utf-8 -*-
"""用账号密码登录 → 抓取商城「个人空间」与「装扮列表」原始 HTML，用于定位解析问题。

用法:
  python tools/fetch_mall.py <account> <password> [mac]

输出:
  %TEMP%\\mall_dump\\space.html, dress_p1.html ... + 关键片段诊断
"""
import base64
import os
import re
import sys
import urllib.parse
import urllib.request
import http.cookiejar

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))
try:
    sys.stdout.reconfigure(encoding="utf-8", errors="replace")
except Exception:
    pass

from verify_login_flow import build_login_body, h2_request, proto_parse, LOGIN_HOST, LOGIN_PORT

UA = "Mozilla/4.0 (compatible; MSIE 7.0; Windows NT 6.2; WOW64; Trident/7.0)"
BASE = "http://gotvgmall.gotvg.cn"
OUT = os.path.join(os.environ.get("TEMP", "."), "mall_dump")


def login(account, pwd, mac):
    body = build_login_body(account, pwd, mac)
    _, resp = h2_request(LOGIN_HOST, LOGIN_PORT, "/login/", body)
    s = resp.decode("ascii")
    dec = base64.urlsafe_b64decode(s + "=" * ((-len(s)) % 4))
    f = proto_parse(dec)
    ret = f.get(1)
    if not ret or ret[1] != 1:
        raise SystemExit("登录失败: %r" % (f.get(2),))
    return f[3][1], f[4][1].decode("ascii")


def main():
    if len(sys.argv) < 3:
        print(__doc__)
        return
    account, pwd = sys.argv[1], sys.argv[2]
    mac = sys.argv[3] if len(sys.argv) > 3 else ""
    uid, token = login(account, pwd, mac)
    print("uid=%d  token=%s" % (uid, token))

    os.makedirs(OUT, exist_ok=True)
    cj = http.cookiejar.CookieJar()
    op = urllib.request.build_opener(urllib.request.HTTPCookieProcessor(cj))
    op.addheaders = [("User-Agent", UA)]

    # 1) 个人空间页：当前佩戴 + 拥有列表首页
    url = "%s/?token=%s&userid=%d&space=1" % (BASE, token, uid)
    html = op.open(url, timeout=25).read().decode("utf-8", "replace")
    with open(os.path.join(OUT, "space.html"), "w", encoding="utf-8") as fh:
        fh.write(html)
    print("space.html = %d bytes" % len(html))

    m = re.search(r'id="default_userAvatar"[^>]*>\s*([^<\s]+)', html)
    worn = m.group(1) if m else ""
    print("worn_url(当前佩戴) = %s" % worn)
    print("space 页 good_item 数 = %d" % html.count('class="good_item"'))

    # 2) 装扮列表翻页（与程序完全相同的参数）
    merged = ""
    for page in range(1, 7):
        form = urllib.parse.urlencode({
            "space": "1", "userid": uid, "token": token, "page": page,
            "good_class": "dress", "userbg_try": "", "user_try": worn,
            "prop_type": "", "page_up_hid": "", "page_down_hid": "1",
            "real_type": "", "item_type": "",
        }).encode()
        req = urllib.request.Request(BASE + "/", data=form)
        req.add_header("User-Agent", UA)
        req.add_header("Content-Type", "application/x-www-form-urlencoded")
        try:
            h = op.open(req, timeout=25).read().decode("utf-8", "replace")
        except Exception as e:
            print("  page %d 请求失败: %s" % (page, e))
            break
        with open(os.path.join(OUT, "dress_p%d.html" % page), "w", encoding="utf-8") as fh:
            fh.write(h)
        cnt = h.count('class="good_item"')
        print("dress_p%d.html = %d bytes, good_item=%d" % (page, len(h), cnt))
        merged += h
        if cnt == 0:
            break

    # 3) 诊断摘要
    print("\n--- 关键词统计（所有列表页合并）---")
    for kw in ["剩余", "永久", "已过期", "additional_exp", "end_time", "buy_msg"]:
        print("  %-16s %d 次" % (kw, merged.count(kw)))

    print("\n--- 含「剩余」或「永久」的原样片段（前 6 处）---")
    hits = [mm.start() for mm in re.finditer(r"剩余|永久|已过期", merged)]
    for i, pos in enumerate(hits[:6]):
        seg = merged[max(0, pos - 160): pos + 120].replace("\n", "")
        print("  [%d] ...%s..." % (i, seg))

    print("\n--- 第一个 good_item 原始结构（用于核对字段）---")
    mm = re.search(r'<div class="good_item">.{0,3000}?class="cont">.{0,600}', merged, re.S)
    if mm:
        print(mm.group(0))


if __name__ == "__main__":
    main()
