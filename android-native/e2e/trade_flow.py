#!/usr/bin/env python3
"""
Android 用户交易闭环 E2E（docs/07 第 1 条 + 6.2 测试点），在已安装 Debug 包的模拟器或真机上运行：

  注册 → 登录 → 空购物车 → 筛选商品 → 详情 → 加购（两家店）→ 改数量 → 库存变化（另一个用户买走一件）→
  确认订单（优惠试算、拆单）→ 连点提交只生成一组订单 → 支付失败（断网）后重新支付 → App 被杀后重进订单重查 →
  商家发货（API）→ 确认收货 → 评价（草稿在 App 被杀后恢复）→ 评价公开可见；另下一单并取消，覆盖全部订单状态。

环境变量：
  BLINK_E2E_HOST_API    电脑访问 API 的地址，默认 http://127.0.0.1:28080/api/v1（必须是测试库，会注册账号、下单）
  BLINK_E2E_DEVICE_API  手机/模拟器访问 API 的地址，默认 http://10.0.2.2:28080/api/v1（真机配合 adb reverse 用 127.0.0.1）
  BLINK_E2E_OUT         截图和报告目录，默认 build/e2e-<时间>
  ANDROID_SERIAL        多台设备时指定设备
结束时恢复设备网络；不会改其他设置。
"""
import os
import sys
import time
import traceback

from api import Api
from device import Device

HOST_API = os.environ.get("BLINK_E2E_HOST_API", "http://127.0.0.1:28080/api/v1")
DEVICE_API = os.environ.get("BLINK_E2E_DEVICE_API", "http://10.0.2.2:28080/api/v1")
OUT = os.environ.get("BLINK_E2E_OUT") or os.path.join(os.path.dirname(__file__), "..", "build", "e2e-" + time.strftime("%Y%m%d-%H%M%S"))
PASSWORD = "Passw0rd!e2e"
EARBUDS = "p_seed_earbuds"   # 数码店，单品 95 折
LAMP = "p_seed_lamp"         # 家居店

api = Api(HOST_API)
dev = Device(os.path.join(OUT, "shots"))
report = []
step_no = [0]


def step(title):
    def wrap(fn):
        def run(*a, **kw):
            step_no[0] += 1
            n = step_no[0]
            t0 = time.time()
            try:
                result = fn(*a, **kw)
            except Exception:
                dev.shot(f"{n:02d}_FAILED")
                report.append(f"| {n} | {title} | 失败 | {time.time() - t0:.1f}s |")
                raise
            dev.shot(f"{n:02d}")
            report.append(f"| {n} | {title} | 通过 | {time.time() - t0:.1f}s |")
            print(f"[{n:02d}] 通过：{title}")
            return result
        return run
    return wrap


def reset_stock(product_id, merchant, quantity):
    """测试前把商品各规格库存设为固定值，脚本可以在同一个库上重复运行。"""
    mt = api.login(merchant, "BlinkDev#2026")
    p = api.ok("GET", f"/merchant/products/{product_id}", mt)
    skus = [{k: s[k] for k in ("sku_id", "sku_name", "price", "specs", "is_default")} | {"stock_quantity": quantity}
            for s in p["skus"]]
    api.ok("PATCH", f"/merchant/products/{product_id}", mt, {"skus": skus})


def user_orders(token, status=""):
    q = "?page_size=100" + (f"&status={status}" if status else "")
    return api.ok("GET", "/orders" + q, token)["items"]


@step("注册新用户（注册即登录），首页右上角显示账号")
def register(username):
    dev.start()
    dev.wait(text="Blink Nova 12")
    dev.tap(rid="account_button")
    dev.tap(rid="tab_register")
    dev.tap(rid="username")
    dev.type(username)
    dev.tap(rid="password")
    dev.type(PASSWORD)
    dev.hide_keyboard()
    dev.tap(rid="submit")
    dev.wait(rid="account_button", text=username)


@step("领券中心领取数码店券（按钮变为“已领取”），“未使用”里出现；设置与帮助可打开")
def claim_coupon():
    dev.tap(rid="account_button")
    dev.tap(rid="row_coupons")
    dev.wait(text="领券中心")
    dev.tap(desc="领取 Blink 数码满 500 减 50")
    dev.wait(text="已领取", timeout=20)
    dev.tap(text="未使用")
    dev.wait(text="Blink 数码满 500 减 50")
    dev.back()
    dev.tap(rid="row_settings")
    dev.tap(text="帮助")
    dev.wait(text="支付有时间限制吗？")
    dev.back()
    dev.back()
    dev.back()
    dev.wait(rid="cart_button")


@step("空购物车提示")
def empty_cart():
    dev.tap(rid="cart_button")
    dev.wait(text="购物车是空的")
    dev.back()


@step("筛选“数码 → 耳机音箱”，进入耳机详情并加入购物车")
def add_earbuds():
    dev.tap(text="数码")
    dev.tap(text="耳机音箱")
    dev.tap(text="Blink Air 降噪耳机")
    dev.wait(rid="add_to_cart", text="加入购物车")
    dev.tap(rid="add_to_cart")
    dev.wait(rid="cart_button", text="购物车 1")
    dev.back()


@step("首页卡片直接加购另一家店的台灯")
def add_lamp_from_list():
    dev.tap(text="全部")
    dev.tap(text="家居")
    dev.wait(text="Blink 护眼台灯 L1")
    dev.tap(desc="把 Blink 护眼台灯 L1 加入购物车")
    dev.wait(rid="cart_button", text="购物车 2")


@step("购物车：两件商品、显示优惠；耳机数量加到库存上限")
def cart_quantities(token):
    stock = api.ok("GET", f"/products/{EARBUDS}")["stock_quantity"]
    dev.tap(rid="cart_button")
    dev.wait(text="Blink Air 降噪耳机")
    dev.wait(rid="cart_discount", contains=True, text="已优惠")
    for _ in range(stock - 1):
        dev.tap(desc="增加 Blink Air 降噪耳机 的数量")
        time.sleep(0.6)
    dev.wait(desc=f"数量 {stock}")
    items = {i["product_id"]: i for i in api.ok("GET", "/cart", token)["items"]}
    assert items[EARBUDS]["quantity"] == stock, items
    return stock


@step("库存变化：另一个用户买走一件耳机，刷新后提示库存不足；减一件后恢复可买")
def stock_changes(token, stock):
    other = "e2e_other_" + str(int(time.time()))
    tok = api.ok("POST", "/auth/register", body={"username": other, "password": PASSWORD})["token"]
    api.ok("POST", "/cart/items", tok, {"product_id": EARBUDS, "quantity": 1})
    order = api.ok("POST", "/orders:checkout", tok, {"idempotency_key": "e2e-other-" + other})["items"][0]
    # 以服务端此刻的判断为准（后台会关闭超时订单回补库存，测试库里的库存可能被别的订单影响）
    item = next(i for i in api.ok("GET", "/cart", token)["items"] if i["product_id"] == EARBUDS)
    assert not item["available"], f"另一个用户下单后耳机应不可买：{item}"
    dev.swipe_down()
    dev.wait(rid="cart_unavailable", text=item["unavailable_reason"])
    dev.tap(desc="减少 Blink Air 降噪耳机 的数量")
    dev.gone(rid="cart_unavailable")
    dev.wait(desc=f"数量 {stock - 1}")
    # 收尾：取消另一个用户的待支付订单，免得它在之后超时关闭时回补库存，影响下次运行
    api.ok("POST", f"/orders/{order['order_id']}:cancel", tok, {})


@step("确认订单：服务端试算、按店铺拆成 2 单、默认自动用券")
def checkout_page():
    dev.tap(rid="checkout_button")
    dev.wait(text="确认订单")
    dev.wait(contains=True, text="将按店铺拆成 2 个订单")
    # 耳机的 95 折不可叠加，不再参与用券；是否用券由服务端试算决定，这里只检查默认方式
    dev.wait(rid="coupon_value", contains=True, text="自动选择最优")


@step("断网提交：结果未知，页面冻结上次提交（提示、不能换券、按钮变为原样重发）")
def submit_offline(token):
    before = len(user_orders(token))
    dev.set_network(False)
    try:
        dev.tap(rid="submit_button")
        dev.wait(rid="pending_banner", contains=True, text="没有收到结果")
        dev.wait(rid="submit_button", text="重新提交上次的订单")
        dev.wait(rid="coupon_value", contains=True, text="上次提交：自动选择最优券")
        dev.tap(rid="coupon_row")
        time.sleep(1)
        assert not dev.find(text="使用优惠券"), "结果未确认时不应能换券"
        assert len(user_orders(token)) == before
    finally:
        dev.set_network(True)
    return before


@step("恢复网络后连点两次“重新提交上次的订单”，只生成一组订单（2 个），进入支付页")
def submit_twice(token, before):
    dev.double_tap_fast(rid="submit_button")
    dev.wait(text="支付订单", timeout=20)
    dev.wait(contains=True, text="共 2 个订单")
    time.sleep(2)
    after = user_orders(token)
    assert len(after) - before == 2, f"订单数 {before} -> {len(after)}"
    assert all(o["status"] == "pending_payment" for o in after[:2])
    assert api.ok("GET", "/cart", token)["items"] == [], "下单后购物车应清空"
    return [o["order_id"] for o in after[:2]]


@step("断网时支付失败，显示原因；恢复网络后重新支付成功")
def pay_fail_then_retry(token, order_ids):
    dev.set_network(False)
    try:
        dev.tap(rid="pay_button")
        dev.wait(rid="pay_result", contains=True, text="网络未连接")
        dev.wait(rid="pay_button", text="重新支付")
        assert all(api.ok("GET", f"/orders/{i}", token)["status"] == "pending_payment" for i in order_ids)
    finally:
        dev.set_network(True)
    dev.tap(rid="pay_button")
    dev.wait(rid="pay_result", contains=True, text="支付成功", timeout=20)
    assert all(api.ok("GET", f"/orders/{i}", token)["status"] == "paid" for i in order_ids)


@step("App 在后台被杀；商家发货后重进：回到支付页并重新查询为“已发货”，订单列表同样")
def kill_and_ship(order_ids):
    restored = dev.kill()
    for merchant in ("blink_merchant", "blink_merchant2"):
        mt = api.login(merchant, "BlinkDev#2026")
        for oid in order_ids:
            status, _ = api.call("PATCH", f"/merchant/orders/{oid}", mt, {"status": "shipped"})
            assert status in (200, 404), status  # 不是本店的订单是 404
    dev.start()
    if restored:
        # 系统恢复被杀前的页面（支付页），页面重新查询订单状态
        dev.wait(text="支付订单")
        assert len(dev.wait(desc="订单状态：已发货")) == 2
        dev.tap(rid="view_orders")
    else:
        dev.tap(rid="account_button")
        dev.tap(rid="row_orders")
    dev.tap(text="已发货")
    dev.wait(text="Blink 数码旗舰店")
    dev.wait(text="Blink 家居生活馆")


@step("确认收货（二次确认）→ 已完成")
def confirm_receipt(token, order_id):
    dev.tap(text="Blink 数码旗舰店")
    dev.wait(text="订单详情")
    dev.tap(rid="primary_action", text="确认收货")
    dev.tap(text="确认收货", nth=-1)
    dev.wait(desc="订单状态：已完成", timeout=20)
    assert api.ok("GET", f"/orders/{order_id}", token)["status"] == "completed"


@step("写评价时 App 被杀，重进后草稿恢复；提交后“已评价”且商品页公开可见")
def review_with_draft(token, order_id, username):
    dev.tap(rid="primary_action", text="去评价")
    dev.wait(text="评价商品")
    dev.tap(desc="4 分")
    dev.tap(rid="review_content")
    dev.type("Quiet and comfortable")
    dev.hide_keyboard()
    dev.tap(rid="review_tags")
    dev.type("noise-cancel")
    dev.hide_keyboard()
    time.sleep(1)
    if dev.kill():
        dev.start()
        # 系统恢复到评价页（重新创建），输入内容还在
        dev.wait(text="评价商品")
        dev.wait(rid="review_content", text="Quiet and comfortable")
        # 离开评价页（页面关闭）再进来：内容来自本地草稿
        dev.tap(rid="back_button")
        dev.wait(text="订单详情")
    else:
        dev.start()
        dev.tap(rid="account_button")
        dev.tap(rid="row_orders")
        dev.tap(text="已完成")
        dev.tap(text="Blink 数码旗舰店")
    dev.tap(rid="primary_action", text="去评价")
    dev.wait(rid="review_content", text="Quiet and comfortable")
    dev.wait(rid="review_tags", text="noise-cancel")
    dev.wait(desc="4 分，已选中")
    dev.tap(rid="review_submit")
    dev.wait(text="订单详情")
    dev.wait(text="已评价")
    reviews = api.ok("GET", f"/products/{EARBUDS}/reviews?page_size=20")["items"]
    mine = [r for r in reviews if r["content"] == "Quiet and comfortable"]
    assert mine and mine[0]["rating"] == 4 and mine[0]["tags"] == ["noise-cancel"], reviews


@step("再下一单后取消（二次确认）→ 已取消；订单列表覆盖全部状态")
def cancel_flow(token):
    dev.restart()
    dev.wait(rid="cart_button")
    dev.tap(text="家居")
    dev.tap(desc="把 Blink 护眼台灯 L1 加入购物车")
    dev.wait(rid="cart_button", text="购物车 1")
    dev.tap(rid="cart_button")
    dev.tap(rid="checkout_button")
    dev.tap(rid="submit_button")
    dev.wait(text="支付订单", timeout=20)
    dev.back()
    dev.wait(text="购物车是空的")  # 下单后购物车已清空
    dev.back()
    dev.tap(rid="account_button")
    dev.tap(rid="row_orders")
    dev.tap(text="待支付")
    dev.tap(text="Blink 家居生活馆")
    dev.tap(rid="secondary_action", text="取消订单")
    dev.tap(text="取消订单", nth=-1)
    dev.wait(desc="订单状态：已取消", timeout=20)
    statuses = {o["status"] for o in user_orders(token)}
    assert {"completed", "shipped", "cancelled"} <= statuses, statuses


@step("首单已在服务端成功但 App 没收到响应、随后被杀：重启后购物车已空，横幅进入恢复，原样重发拿到同一组订单")
def recover_lost_response(token, username):
    me = api.ok("GET", "/auth/me", token)
    api.ok("POST", "/cart/items", token, {"product_id": LAMP, "quantity": 1})
    expected = api.ok("GET", "/cart/discount-preview", token)["pay_amount"]
    before = len(user_orders(token))
    key = "e2e-lost-" + str(int(time.time()))
    # 服务端已经成功下单（购物车被清空）……
    first = api.ok("POST", "/orders:checkout", token, {"idempotency_key": key, "expected_pay_amount": expected})
    assert api.ok("GET", "/cart", token)["items"] == []
    # ……但 App 没收到响应、进程被杀：本地只留下冻结的那次提交（与 App 提交前写入的格式相同）
    dev.write_pending_checkout(me["account_id"], {"key": key, "expected_pay_amount": expected})
    dev.restart()
    dev.tap(rid="cart_button")
    dev.wait(text="购物车是空的")
    dev.tap(rid="pending_checkout")
    dev.wait(rid="pending_banner", contains=True, text="没有收到结果")
    dev.wait(rid="submit_button", text="重新提交上次的订单")
    dev.tap(rid="submit_button")
    dev.wait(text="支付订单", timeout=20)
    dev.wait(contains=True, text=first["items"][0]["order_no"])
    after = user_orders(token)
    assert len(after) == before + len(first["items"]), f"订单数 {before} -> {len(after)}"
    # 恢复后不再提示
    dev.back()
    dev.wait(text="购物车是空的")
    assert not dev.find(rid="pending_checkout"), "恢复后横幅应消失"
    for o in first["items"]:
        api.ok("POST", f"/orders/{o['order_id']}:cancel", token, {})


def main():
    username = "e2e_" + str(int(time.time()))
    print("输出目录：", os.path.abspath(OUT))
    reset_stock(EARBUDS, "blink_merchant", 5)
    reset_stock(LAMP, "blink_merchant2", 20)
    dev.reset_app(DEVICE_API)
    ok = False
    try:
        register(username)
        token = api.login(username, PASSWORD)
        claim_coupon()
        empty_cart()
        add_earbuds()
        add_lamp_from_list()
        stock = cart_quantities(token)
        stock_changes(token, stock)
        checkout_page()
        before = submit_offline(token)
        order_ids = submit_twice(token, before)
        pay_fail_then_retry(token, order_ids)
        kill_and_ship(order_ids)
        digital = next(o["order_id"] for o in user_orders(token) if o["merchant_name"] == "Blink 数码旗舰店")
        confirm_receipt(token, digital)
        review_with_draft(token, digital, username)
        cancel_flow(token)
        recover_lost_response(token, username)
        ok = True
    except Exception:
        traceback.print_exc()
    finally:
        dev.set_network(True)
        with open(os.path.join(OUT, "report.md"), "w") as f:
            f.write(f"# Android 交易闭环 E2E\n\n- 时间：{time.strftime('%Y-%m-%d %H:%M:%S')}\n- 账号：{username}\n"
                    f"- 设备 API：{DEVICE_API}\n- 结果：{'通过' if ok else '失败'}\n\n| # | 步骤 | 结果 | 用时 |\n| --- | --- | --- | --- |\n")
            f.write("\n".join(report) + "\n")
    print("结果：", "通过" if ok else "失败", "；报告：", os.path.join(os.path.abspath(OUT), "report.md"))
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
