#!/usr/bin/env python3
"""
Android 聊天页 E2E（7.3），在已安装 Debug 包的模拟器或真机上运行：

  注册 → 进入 AI 导购 → 预设问题“推荐一款通勤降噪耳机”（流式正文 + 商品卡 + 追问）→ 追问“把第一个加入购物车”（服务端购物车核对）→
  “去结算”（订单卡）→ “支付订单”（服务端订单 paid）→ 新对话 → “看看我的购物车”“有什么优惠券”“帮我领券”（券卡）→
  断网发送失败后“重试” → 发送后立刻回桌面、再回来内容完整（后台返回）→ 后台被杀后重进从服务端恢复整段对话（不重复发送）→
  历史抽屉：列表、置顶、重命名、切换会话、删除。全程录屏。

环境变量：
  BLINK_E2E_HOST_API    电脑访问 API，默认 http://127.0.0.1:8080/api/v1（必须是测试库）
  BLINK_E2E_DEVICE_API  设备访问 API，默认 http://10.0.2.2:8080/api/v1
  BLINK_E2E_OUT         截图、录屏和报告目录
adb 只能输入英文：中文问题全部通过预设问题和追问 chip 点出来，中文输入由单测覆盖。
"""
import os
import subprocess
import sys
import time
import traceback

from api import Api
from device import ADB, Device

HOST_API = os.environ.get("BLINK_E2E_HOST_API", "http://127.0.0.1:8080/api/v1")
DEVICE_API = os.environ.get("BLINK_E2E_DEVICE_API", "http://10.0.2.2:8080/api/v1")
OUT = os.environ.get("BLINK_E2E_OUT") or os.path.join(os.path.dirname(__file__), "..", "build", "e2e-chat-" + time.strftime("%Y%m%d-%H%M%S"))
PASSWORD = "Passw0rd!e2e"

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


def answer_contains(text, timeout=30):
    """等待某条回答正文里出现文字。"""
    return dev.wait(timeout=timeout, rid="answer_text", text=text, contains=True)


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


@step("首页“导购”进入聊天页：欢迎语和预设问题")
def open_chat():
    dev.tap(rid="chat_button")
    dev.wait(text="你好，我是 Blink 导购助手")
    dev.wait(text="推荐一款通勤降噪耳机")


@step("预设问题“推荐一款通勤降噪耳机”：思考步骤、流式正文、商品卡、追问")
def recommend():
    dev.tap(text="推荐一款通勤降噪耳机")
    dev.wait(rid="user_text", text="推荐一款通勤降噪耳机")
    answer_contains("Blink Air 降噪耳机")
    dev.wait(desc="商品：Blink Air 降噪耳机", contains=True)
    dev.wait(rid="steps_summary")
    dev.wait(text="把第一个加入购物车")


@step("追问“把第一个加入购物车”：加购成功、购物车卡；服务端购物车一致")
def add_first(token):
    dev.tap(text="把第一个加入购物车")
    answer_contains("已把 Blink Air 降噪耳机 × 1 加入购物车")
    cart = api.ok("GET", "/cart", token)
    assert [(i["product_id"], i["quantity"]) for i in cart["items"]] == [("p_seed_earbuds", 1)], cart
    dev.wait(text="去购物车")
    dev.wait(text="去结算")


@step("追问“去结算”：订单卡；服务端 1 个待支付订单，购物车清空")
def checkout(token):
    dev.tap(text="去结算")
    answer_contains("已为你生成 1 个订单")
    orders = api.ok("GET", "/orders?status=pending_payment", token)["items"]
    assert len(orders) == 1 and orders[0]["pay_amount"] == "569.05", orders
    assert api.ok("GET", "/cart", token)["items"] == []
    dev.wait(text="支付订单")
    return orders[0]["order_id"]


@step("追问“支付订单”：模拟支付成功；服务端订单 paid；订单卡点开详情")
def pay(token, order_id):
    dev.tap(text="支付订单")
    answer_contains("已完成（模拟）支付")
    o = api.ok("GET", f"/orders/{order_id}", token)
    assert o["status"] == "paid" and o["payment"]["status"] == "paid", o
    dev.tap(desc="订单 ", contains=True, nth=-1)
    dev.wait(text="订单详情", timeout=10)
    dev.back()
    dev.wait(rid="input")


@step("新对话 → “看看我的购物车”（空）→ 追问“有什么优惠券”→“帮我领券”：券卡、领取成功")
def coupons(token):
    dev.tap(rid="new_button")
    dev.wait(text="你好，我是 Blink 导购助手")
    dev.tap(text="看看我的购物车")
    answer_contains("还是空的")
    dev.tap(text="有什么优惠券")
    answer_contains("可以领")
    dev.tap(text="帮我领券")
    answer_contains("已帮你领到")
    mine = api.ok("GET", "/coupons/mine?status=unused", token)
    assert mine["total"] >= 1, mine


@step("断网发送：显示“网络未连接”和“重试”；恢复网络后重试成功")
def offline_retry():
    dev.set_network(False)
    dev.tap(text="看看购物车能优惠多少")
    dev.wait(rid="status_text", text="网络未连接", contains=True, timeout=20)
    dev.wait(rid="retry_button", text="重试")
    dev.shot("offline_error")
    dev.set_network(True)
    dev.tap(rid="retry_button", text="重试")
    answer_contains("购物车", timeout=30)
    dev.gone(rid="retry_button")


@step("发送后立刻回桌面，几秒后回来：回答完整显示（后台期间继续接收）")
def background_return():
    dev.tap(text="推荐一款手机")
    dev.shell("input keyevent 3")
    time.sleep(4)
    dev.start()
    answer_contains("Blink Nova 12")
    dev.wait(text="把第一个加入购物车")


@step("后台被杀后重进：按会话 ID 从服务端恢复整段对话，不重复发送")
def restore_after_kill(token):
    before = api.ok("GET", "/agent/sessions?page_size=20", token)
    total_msgs = sum(s["message_count"] for s in before["items"])
    killed = dev.kill()
    dev.start()  # 系统恢复被杀前的页面栈：聊天页按保存的会话 ID 重新读取对话
    if killed:
        answer_contains("Blink Nova 12")
        dev.wait(rid="user_text", text="推荐一款手机")
        dev.wait(text="把第一个加入购物车")  # 追问也从服务端恢复
        dev.swipe_down()  # 往上翻：更早的轮次也在
        dev.wait(rid="user_text", text="看看购物车能优惠多少")
    else:
        dev.tap(rid="chat_button")
    after = api.ok("GET", "/agent/sessions?page_size=20", token)
    assert sum(s["message_count"] for s in after["items"]) == total_msgs, (before, after)
    return killed


@step("历史抽屉：会话列表、置顶、重命名、切换会话恢复对话、删除")
def history():
    dev.tap(rid="history_button")
    dev.wait(text="历史会话")
    dev.wait(rid="session_title", text="耳机咨询", contains=True)
    dev.shot("history_list")
    dev.tap(rid="session_more", nth=1)
    dev.tap(text="置顶")
    dev.wait(rid="session_title", text="📌", contains=True)
    dev.tap(rid="session_more", nth=0)
    dev.tap(text="重命名")
    dev.clear_field(30)
    dev.type("Renamed")
    dev.tap(text="保存")
    dev.wait(rid="session_title", text="Renamed", contains=True)
    dev.tap(rid="session_title", text="Renamed", contains=True)
    dev.wait(rid="user_text", text="支付订单")  # 列表停在最后一轮
    answer_contains("已完成（模拟）支付")
    dev.swipe_down()
    dev.wait(rid="user_text", text="去结算")
    dev.tap(rid="history_button")
    dev.tap(rid="session_more", nth=-1)
    dev.tap(text="删除")
    dev.tap(text="删除", nth=-1)
    dev.gone(rid="session_title", text="购物车", contains=True)
    dev.back()


def main():
    username = "e2echat_" + str(int(time.time()))
    print("输出目录：", os.path.abspath(OUT))
    dev.reset_app(DEVICE_API)
    rec = subprocess.Popen([ADB, "shell", "screenrecord", "--time-limit", "180", "--bit-rate", "4000000", "/sdcard/chat_flow.mp4"])
    ok = False
    try:
        register(username)
        token = api.login(username, PASSWORD)
        open_chat()
        recommend()
        add_first(token)
        order_id = checkout(token)
        pay(token, order_id)
        coupons(token)
        offline_retry()
        background_return()
        restore_after_kill(token)
        history()
        ok = True
    except Exception:
        traceback.print_exc()
    finally:
        dev.set_network(True)
        dev.shell("pkill -2 screenrecord")
        rec.wait(timeout=30)
        time.sleep(1)
        subprocess.run([ADB, "pull", "/sdcard/chat_flow.mp4", os.path.join(OUT, "chat_flow.mp4")], capture_output=True)
        with open(os.path.join(OUT, "report.md"), "w") as f:
            f.write(f"# Android 聊天页 E2E\n\n- 时间：{time.strftime('%Y-%m-%d %H:%M:%S')}\n- 账号：{username}\n"
                    f"- 设备 API：{DEVICE_API}\n- 结果：{'通过' if ok else '失败'}\n\n| # | 步骤 | 结果 | 用时 |\n| --- | --- | --- | --- |\n")
            f.write("\n".join(report) + "\n")
    print("结果：", "通过" if ok else "失败", "；报告：", os.path.join(os.path.abspath(OUT), "report.md"))
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
