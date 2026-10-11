#!/usr/bin/env python3
"""docs/07 E2E 验收清单第 2–7 条，对部署好的环境逐条检查（第 1 条交易闭环由 Web E2E 与 Android trade_flow 覆盖，第 8 条由 Android 脚本覆盖）。

  ACCEPT_BASE           主验收部署（默认 http://127.0.0.1:18088/api/v1）
  ACCEPT_DEGRADED_BASE  第 4 条：不配 AI / Milvus / MinIO 的实例；不设则跳过
  ACCEPT_SLOW_BASE      第 3 条取消 / 第 7 条：模型服务很慢的实例（运行会停在模型调用上）；不设则跳过
  ACCEPT_MYSQL_DB       第 6 条过期券：直接在验收库插一张已过期的券（docker exec blink_shop-mysql-1）；不设则跳过

只依赖标准库。结果写到 stdout（Markdown），有失败时退出码 1。会注册 acc_* 账号、下单、评价，只对验收库运行。
"""
import json, os, subprocess, sys, threading, time, uuid, urllib.request, urllib.error

BASE = os.environ.get("ACCEPT_BASE", "http://127.0.0.1:18088/api/v1")
DEGRADED = os.environ.get("ACCEPT_DEGRADED_BASE", "")
SLOW = os.environ.get("ACCEPT_SLOW_BASE", "")
MYSQL_DB = os.environ.get("ACCEPT_MYSQL_DB", "")
PW = "BlinkDev#2026"
results = []  # (item, name, ok, detail)


def req(method, path, token=None, body=None, base=None, raw=None, headers=None, timeout=60):
    url = (base or BASE) + path
    data = raw if raw is not None else (json.dumps(body).encode() if body is not None else None)
    h = dict(headers or {})
    if body is not None and raw is None:
        h["Content-Type"] = "application/json"
    if token:
        h["Authorization"] = "Bearer " + token
    r = urllib.request.Request(url, data=data, method=method, headers=h)
    try:
        with urllib.request.urlopen(r, timeout=timeout) as resp:
            data = resp.read()
            try:
                text = data.decode()
            except UnicodeDecodeError:
                return resp.status, data  # 文件下载等二进制响应
            return resp.status, (json.loads(text) if text.startswith(("{", "[")) else text)
    except urllib.error.HTTPError as e:
        text = e.read().decode()
        try:
            return e.code, json.loads(text)
        except ValueError:
            return e.code, text


def check(item, name, ok, detail=""):
    results.append((item, name, None if ok is None else bool(ok), detail))


def login(username, base=None):
    s, b = req("POST", "/auth/login", body={"username": username, "password": PW}, base=base)
    assert s == 200, (username, s, b)
    return b["token"]


def register(prefix, base=None):
    name = f"acc_{prefix}_{uuid.uuid4().hex[:8]}"
    s, b = req("POST", "/auth/register", body={"username": name, "password": "Accept-Test#2026", "display_name": "验收"}, base=base)
    assert s == 201, (s, b)
    return b["token"], b["account"]["account_id"]


def stock(pid, sku, base=None):
    s, b = req("GET", f"/products/{pid}/skus", base=base)
    return next(x["stock_quantity"] for x in b["items"] if x["sku_id"] == sku)


def order_count(tok, base=None):
    return req("GET", "/orders?page_size=1", tok, base=base)[1]["total"]


def stream(tok, sid, content, client_id, base=None, on_start=None):
    """发一条消息，返回事件列表 [(event, data)]。"""
    url = (base or BASE) + f"/agent/sessions/{sid}/messages:stream"
    r = urllib.request.Request(url, data=json.dumps({"client_message_id": client_id, "content": content}).encode(), method="POST",
                               headers={"Content-Type": "application/json", "Authorization": "Bearer " + tok, "Accept": "text/event-stream"})
    events, ev = [], None
    with urllib.request.urlopen(r, timeout=120) as resp:
        for line in resp:
            line = line.decode().rstrip("\n")
            if line.startswith("event: "):
                ev = line[7:]
            elif line.startswith("data: ") and ev:
                data = json.loads(line[6:])
                events.append((ev, data))
                if ev == "message_start" and on_start:
                    on_start(data)
                if ev in ("message_done", "error"):
                    break
    return events


def new_session(tok, base=None):
    return req("POST", "/agent/sessions", tok, body={}, base=base)[1]["session_id"]


def concurrently(fns):
    out = [None] * len(fns)
    gate = threading.Event()

    def run(i, f):
        gate.wait()
        out[i] = f()
    ts = [threading.Thread(target=run, args=(i, f)) for i, f in enumerate(fns)]
    for t in ts:
        t.start()
    gate.set()
    for t in ts:
        t.join()
    return out


# ---------- 第 2 条：并发结算 ----------
def item2():
    tok, _ = register("dup")
    req("POST", "/cart/items", tok, {"product_id": "p_seed_mouse", "sku_id": "sku_seed_mouse_gray", "quantity": 1})
    before = order_count(tok)
    codes = concurrently([lambda k=k: req("POST", "/orders:checkout", tok, {"idempotency_key": f"acc-{k}-{uuid.uuid4().hex[:6]}"})[0] for k in range(2)])
    check(2, "同一购物车两个幂等键同时结算：只成功一次、只多一组订单", sorted(codes)[0] == 201 and codes.count(201) == 1 and order_count(tok) == before + 1, f"状态码 {codes}")
    # 库存不足：两人各把键盘全部库存加购（其中一人购物车里还有鼠标），同时结算
    # 挑一个有库存的商品（耳机、台灯留给 Android 交易脚本，鼠标作为购物车里的“另一件商品”）
    pid, sku = None, None
    for cand in ("p_seed_keyboard", "p_seed_nova", "p_seed_vista"):
        s, b = req("GET", f"/products/{cand}/skus")
        stocked = [x for x in b["items"] if x["stock_quantity"] > 0]
        if stocked:
            pid, sku = cand, stocked[0]
            break
    if not sku:
        check(2, "库存不足时无半成品订单", False, "候选商品都没有库存，无法构造")
        return
    n = sku["stock_quantity"]
    a, _ = register("stockA")
    c, _ = register("stockB")
    for t in (a, c):
        s, b = req("POST", "/cart/items", t, {"product_id": pid, "sku_id": sku["sku_id"], "quantity": n})
        assert s == 200, (s, b)
    req("POST", "/cart/items", c, {"product_id": "p_seed_mouse", "sku_id": "sku_seed_mouse_gray", "quantity": 1})
    mouse_before = stock("p_seed_mouse", "sku_seed_mouse_gray")
    res = concurrently([lambda t=t: req("POST", "/orders:checkout", t, {"idempotency_key": "acc-stock-" + uuid.uuid4().hex[:6]}) for t in (a, c)])
    codes = [r[0] for r in res]
    after = stock(pid, sku["sku_id"])
    loser = [t for t, r in zip((a, c), res) if r[0] != 201]
    loser_orders = order_count(loser[0]) if loser else -1
    # 失败的一方（不管是不是带鼠标的那位）没有任何订单；鼠标库存只在 c 成功时减少
    mouse_after = stock("p_seed_mouse", "sku_seed_mouse_gray")
    c_ok = res[1][0] == 201
    check(2, "库存只够一人：一单成功、另一单 409，库存为 0 不为负，失败方无半成品订单（含购物车里的其他商品）",
          codes.count(201) == 1 and any(x == 409 for x in codes) and after == 0 and loser_orders == 0 and mouse_after == mouse_before - (1 if c_ok else 0),
          f"状态码 {codes}，错误码 {[r[1].get('code') for r in res if r[0] != 201]}，{pid} 库存 {n}→{after}，鼠标 {mouse_before}→{mouse_after}")


# ---------- 第 3 条：重复消息与取消 ----------
def item3():
    tok, _ = register("msg")
    sid = new_session(tok)
    first = stream(tok, sid, "推荐一款静音无线鼠标", "acc-same-1")
    second = stream(tok, sid, "推荐一款静音无线鼠标", "acc-same-1")
    s, got = req("GET", f"/agent/sessions/{sid}", tok)
    count, msgs = got["session"]["message_count"], got["messages"]
    run1, run2 = first[0][1]["run_id"], second[0][1]["run_id"]
    check(3, "相同 client_message_id 重发：返回同一次运行（replayed），会话里只有一条用户消息",
          run1 == run2 and second[0][1].get("replayed") is True and count == 1 and len(msgs) == 1,
          f"run {run1} / {run2}，replayed={second[0][1].get('replayed')}，message_count={count}，消息 {len(msgs)} 条")
    if not SLOW:
        check(3, "取消 SSE 后运行为 cancelled", None, "跳过：没有设置 ACCEPT_SLOW_BASE")
        return
    tok, _ = register("cancel", base=SLOW)
    sid = new_session(tok, base=SLOW)
    started = {}

    def cancel_soon(data):
        started.update(data)
        threading.Timer(0.8, lambda: req("POST", f"/agent/runs/{data['run_id']}:cancel", tok, body={}, base=SLOW)).start()
    t0 = time.time()
    evs = stream(tok, sid, "推荐一款通勤降噪耳机", "acc-cancel-1", base=SLOW, on_start=cancel_soon)
    took = time.time() - t0
    time.sleep(0.5)
    s, trace = req("GET", f"/agent/runs/{started['run_id']}/trace", tok, base=SLOW)
    last = evs[-1]
    check(3, "运行中取消：SSE 以 error(cancelled) 结束，运行状态 cancelled，不用等模型超时",
          last[0] == "error" and trace["run"]["status"] == "cancelled" and took < 10,
          f"最后事件 {last[0]} {last[1].get('code')}，状态 {trace['run']['status']}，耗时 {took:.1f}s")


# ---------- 第 4 条：无 AI / Milvus / MinIO ----------
def item4():
    if not DEGRADED:
        check(4, "降级实例", None, "跳过：没有设置 ACCEPT_DEGRADED_BASE")
        return
    s, b = req("GET", "/products?page_size=3", base=DEGRADED)
    check(4, "商品浏览可用", s == 200 and b["total"] > 0, f"{s}，共 {b.get('total')} 件")
    tok, _ = register("deg", base=DEGRADED)
    req("POST", "/cart/items", tok, {"product_id": "p_seed_mouse", "sku_id": "sku_seed_mouse_gray", "quantity": 1}, base=DEGRADED)
    s, b = req("POST", "/orders:checkout", tok, {"idempotency_key": "acc-deg-" + uuid.uuid4().hex[:6]}, base=DEGRADED)
    oid = b["items"][0]["order_id"] if s == 201 else ""
    ps, _ = req("POST", f"/orders/{oid}:pay", tok, body={}, base=DEGRADED) if oid else (0, None)
    check(4, "下单、支付可用", s == 201 and ps == 200, f"结算 {s}，支付 {ps}")
    sid = new_session(tok, base=DEGRADED)
    evs = stream(tok, sid, "耳机保修多久", "acc-deg-k", base=DEGRADED)
    blocks = [d["block"] for e, d in evs if e == "block"]
    cites = [c for b2 in blocks if b2.get("type") == "citation" for c in b2.get("citations", [])]
    check(4, "导购（无模型）用关键词检索回答，引用都是真实分块", evs[-1][0] == "message_done" and cites and all(c.get("chunk_id") for c in cites),
          f"{len(cites)} 条引用：{[c['chunk_id'] for c in cites][:3]}")
    evs = stream(tok, sid, "扫地机器人的滤网多久换一次", "acc-deg-none", base=DEGRADED)
    text = "".join(d.get("delta", "") for e, d in evs if e == "text_delta")
    blocks = [d["block"] for e, d in evs if e == "block"]
    check(4, "资料库里没有的问题如实说明、不给引用", evs[-1][0] == "message_done" and not any(b2.get("type") == "citation" for b2 in blocks) and "没有找到" in text, text[:60])
    s, b = req("POST", "/files", tok, raw=multipart(png_bytes()), headers={"Content-Type": MP_CT}, base=DEGRADED)
    check(4, "上传返回规范错误（503 object_storage_unavailable）", s == 503 and b.get("code") == "object_storage_unavailable", f"{s} {b.get('code') if isinstance(b, dict) else b}")
    s, b = req("POST", "/search/image", tok, {"file_id": "file_doesnotexist0001"}, base=DEGRADED)
    check(4, "图搜返回规范错误（503 image_search_unavailable）", s == 503 and b.get("code") == "image_search_unavailable", f"{s} {b.get('code') if isinstance(b, dict) else b}")


MP_CT = "multipart/form-data; boundary=acceptboundary"


def multipart(data, filename="x.png", ctype="image/png"):
    return (f"--acceptboundary\r\nContent-Disposition: form-data; name=\"file\"; filename=\"{filename}\"\r\nContent-Type: {ctype}\r\n\r\n").encode() + data + b"\r\n--acceptboundary--\r\n"


def png_bytes():
    import zlib, struct
    def chunk(t, d):
        return struct.pack(">I", len(d)) + t + d + struct.pack(">I", zlib.crc32(t + d) & 0xffffffff)
    raw = b"".join(b"\x00" + b"\xff\x00\x00" * 4 for _ in range(4))
    return b"\x89PNG\r\n\x1a\n" + chunk(b"IHDR", struct.pack(">IIBBBBB", 4, 4, 8, 2, 0, 0, 0)) + chunk(b"IDAT", zlib.compress(raw)) + chunk(b"IEND", b"")


# ---------- 第 5 条：越权 ----------
def item5():
    a, _ = register("isoA")
    b_, _ = register("isoB")
    s, f = req("POST", "/files", a, raw=multipart(png_bytes()), headers={"Content-Type": MP_CT})
    fid = f["file"]["file_id"] if s == 201 else ""
    s1, _ = req("GET", f"/files/{fid}", a)
    s2, _ = req("GET", f"/files/{fid}", b_)
    check(5, "用户 B 读不到用户 A 的文件", s1 == 200 and s2 in (403, 404), f"A {s1} / B {s2}")
    sid = new_session(a)
    s, _ = req("GET", f"/agent/sessions/{sid}", b_)
    check(5, "用户 B 读不到用户 A 的会话", s == 404, str(s))
    s, cart = req("POST", "/cart/items", a, {"product_id": "p_seed_mouse", "sku_id": "sku_seed_mouse_gray", "quantity": 1})
    item = cart["items"][0]["cart_item_id"]
    s, _ = req("PATCH", f"/cart/items/{item}", b_, {"quantity": 3})
    s3, _ = req("DELETE", f"/cart/items/{item}", b_)
    check(5, "用户 B 改不了、删不了用户 A 的购物车", s == 404 and s3 == 404, f"PATCH {s} / DELETE {s3}")
    s, o = req("POST", "/orders:checkout", a, {"idempotency_key": "acc-iso-" + uuid.uuid4().hex[:6]})
    oid = o["items"][0]["order_id"]
    s, _ = req("GET", f"/orders/{oid}", b_)
    s4, _ = req("POST", f"/orders/{oid}:cancel", b_, {"reason": "x"})
    check(5, "用户 B 读不到、取消不了用户 A 的订单", s == 404 and s4 == 404, f"GET {s} / 取消 {s4}")
    m2 = login("blink_merchant2")
    s, b = req("PATCH", "/merchant/products/p_seed_mouse", m2, {"name": "被改了"})
    check(5, "商家 B 改不了商家 A 的商品", s in (403, 404), f"{s} {b.get('code') if isinstance(b, dict) else ''}")
    s, _ = req("GET", "/admin/configs", a)
    s5, _ = req("GET", "/admin/agent/runs", a)
    check(5, "普通用户访问不了 /admin/*", s == 403 and s5 == 403, f"configs {s} / runs {s5}")


# ---------- 第 6 条：非法输入 ----------
def item6():
    tok, _ = register("bad")
    s, b = req("POST", "/files", tok, raw=multipart(b"#!/bin/sh\necho hacked\n" * 10, "photo.png", "image/png"), headers={"Content-Type": MP_CT})
    check(6, "伪造 MIME（脚本内容声称 image/png）被拒", s in (400, 415) and isinstance(b, dict), f"{s} {b.get('code') if isinstance(b, dict) else b}")
    big = png_bytes() + b"\x00" * (10 * 1024 * 1024 + 10)
    s, b = req("POST", "/files", tok, raw=multipart(big), headers={"Content-Type": MP_CT})
    check(6, "超限文件（>10MB）被拒", s == 413, f"{s} {b.get('code') if isinstance(b, dict) else ''}")
    m = login("blink_merchant")
    s, b = req("POST", "/merchant/unstructured-ingestions", m, {"title": "内网", "source_type": "web", "source_url": "http://127.0.0.1:8080/admin"})
    s2, b2 = req("POST", "/merchant/unstructured-ingestions", m, {"title": "元数据", "source_type": "web", "source_url": "http://169.254.169.254/latest/meta-data/"})
    check(6, "恶意 URL（本机、云元数据地址）采集被拒并定位到字段", s == 400 and s2 == 400 and b.get("field") == "source_url",
          f"{s} {b.get('code')}/{b.get('field')}；{s2} {b2.get('code')}")
    req("POST", "/cart/items", tok, {"product_id": "p_seed_mouse", "sku_id": "sku_seed_mouse_gray", "quantity": 1})
    s, o = req("POST", "/orders:checkout", tok, {"idempotency_key": "acc-bad-" + uuid.uuid4().hex[:6]})
    oid = o["items"][0]["order_id"]
    s, b = req("POST", f"/orders/{oid}:confirm-receipt", tok, body={})
    check(6, "非法订单状态迁移（未支付就确认收货）被拒，错误码稳定", s == 409, f"{s} {b.get('code')}")
    if MYSQL_DB:
        cid = "coupon_acc_expired_" + uuid.uuid4().hex[:6]
        sql = (f"INSERT INTO {MYSQL_DB}.coupons (coupon_id,name,scope,merchant_id,type,threshold_amount,discount_amount,total_count,claimed_count,per_user_limit,start_at,end_at,status) "
               f"VALUES ('{cid}','验收过期券','platform','','fixed_amount',0,5,100,0,1,NOW(3) - INTERVAL 10 DAY,NOW(3) - INTERVAL 1 DAY,'active')")
        subprocess.run(["docker", "exec", "blink_shop-mysql-1", "sh", "-c", f'mysql -uroot -p"$MYSQL_ROOT_PASSWORD" -e "{sql}"'], check=True, capture_output=True)
        s, b = req("POST", f"/coupons/{cid}:claim", tok, body={})
        check(6, "过期券领取被拒", s == 409 and b.get("code") == "coupon_unavailable", f"{s} {b.get('code')}")
    else:
        check(6, "过期券", None, "跳过：没有设置 ACCEPT_MYSQL_DB")
    u = login("blink_user")
    path = "/orders/o_seed_completed/items/o_seed_completed_item2:review"
    s1, b1 = req("POST", path, u, {"rating": 5, "content": "acceptance review"})
    s2, b2 = req("POST", path, u, {"rating": 4, "content": "again"})
    check(6, "重复评价被拒（同一件只能评价一次）", s1 in (200, 201, 409) and s2 == 409, f"第一次 {s1}，第二次 {s2} {b2.get('code')}")


# ---------- 第 7 条：Prompt / 配置 ----------
def item7():
    admin = login("blink_admin")
    s, lst = req("GET", "/admin/configs", admin)
    secrets = [c for c in lst["items"] if c["secret"]]
    leaked = [c["key"] for c in secrets if c["value"] not in ("", "******")]
    check(7, "配置列表里的密钥都是掩码", s == 200 and secrets and not leaked, f"{len(secrets)} 项密钥，明文 {leaked or '无'}")
    s, b = req("PATCH", "/admin/configs/risk.blocked_words", admin, {"value": "违法,违禁,假货,绕过风控,验收测试词", "reason": "验收"})
    tok, _ = register("cfg")
    sid = new_session(tok)
    evs = stream(tok, sid, "这里有验收测试词吗", "acc-cfg-1")
    run_id = evs[0][1]["run_id"]
    s2, tr = req("GET", f"/agent/runs/{run_id}/trace", tok)
    blocked = any(i["stage"] == "risk" and i["status"] == "blocked" for i in tr["items"])
    req("PATCH", "/admin/configs/risk.blocked_words", admin, {"value": "", "reason": "验收恢复"})
    check(7, "管理员改配置后立即生效（新加的风险词拦截了下一次运行）", s == 200 and blocked, f"PATCH {s}，轨迹 risk=blocked: {blocked}")
    if not SLOW:
        check(7, "发布 Prompt 后运行关联生效版本", None, "跳过：没有设置 ACCEPT_SLOW_BASE（需要走模型的实例）")
        return
    admin_s = login("blink_admin", base=SLOW)
    s, d = req("GET", "/admin/prompts/agent.planner", admin_s, base=SLOW)
    active = d["active_version"]
    req("PATCH", "/admin/prompts/agent.planner", admin_s, {"content": "验收版本：你是意图规划器，只输出 JSON。"}, base=SLOW)
    s, d = req("GET", "/admin/prompts/agent.planner", admin_s, base=SLOW)
    draft = d["draft"]["version"]
    s, pub = req("POST", "/admin/prompts/agent.planner/publish", admin_s, {"version": draft, "expected_active_version": active, "reason": "验收"}, base=SLOW)
    tok, _ = register("prm", base=SLOW)
    sid = new_session(tok, base=SLOW)
    evs = stream(tok, sid, "你好", "acc-prm-1", base=SLOW)
    run_id = evs[0][1]["run_id"]
    s2, tr = req("GET", f"/agent/runs/{run_id}/trace", tok, base=SLOW)
    planner = [i for i in tr["items"] if i["stage"] == "planner" and i["event_type"] == "model"]
    s3, run = req("GET", f"/admin/agent/runs/{run_id}", admin_s, base=SLOW)
    got = planner[0]["metadata"].get("prompt_version") if planner else None
    req("POST", "/admin/prompts/agent.planner/rollback", admin_s, {"version": 0, "expected_active_version": draft, "reason": "验收恢复"}, base=SLOW)
    check(7, "发布 Prompt 后运行的轨迹和运行记录都关联生效版本", s == 200 and got == draft and run.get("prompt_versions", {}).get("agent.planner") == draft,
          f"发布 v{draft}：轨迹 planner.model prompt_version={got}，运行 prompt_versions={run.get('prompt_versions')}")


def main():
    for fn in (item2, item3, item4, item5, item6, item7):
        try:
            fn()
        except Exception as e:  # 单条出错记为失败，继续后面的
            check(int(fn.__name__[4:]), f"{fn.__name__} 执行出错", False, repr(e)[:200])
    print(f"# 第 07 章验收清单 2–7 条\n\n- 主部署：{BASE}\n- 降级实例：{DEGRADED or '未设置'}\n- 慢模型实例：{SLOW or '未设置'}\n- 时间：{time.strftime('%Y-%m-%d %H:%M:%S %z')}\n")
    print("| 条目 | 检查 | 结果 | 说明 |\n| --- | --- | --- | --- |")
    failed = 0
    for item, name, ok, detail in results:
        mark = "通过" if ok else ("跳过" if ok is None else "**失败**")
        if ok is False:
            failed += 1
        print(f"| {item} | {name} | {mark} | {str(detail).replace('|', '/')} |")
    print(f"\n共 {len(results)} 项，失败 {failed} 项")
    sys.exit(1 if failed else 0)


if __name__ == "__main__":
    main()
