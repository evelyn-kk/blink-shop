#!/usr/bin/env python3
"""
Android 聊天附图 E2E（8.3），在已安装 Debug 包的模拟器或真机上运行：

  注册 → 进入 AI 导购 → 拍照：拒绝相机权限（提示可改用相册）→ 再次拒绝（“不再询问”：提示去系统设置并能打开设置页）→
  授权后拍照（系统相机应用）→ 预览“图片已就绪” → 只发图片 → 用户气泡“附带 1 张图片”、导购给出按图检索的结果（模拟器相机画面
  与商品都不像：只给“外观相近”参考，不推荐）→ 从相册选商品照片（台灯）→ 移除 → 重新选 → 发送 → “按图片找到的商品”第一件是台灯 →
  追问“把第一个加入购物车”（服务端购物车核对）。

需要后端配置了图片搜索（MILVUS_ADDR）和对象存储（MINIO_ENDPOINT），并用测试库。相机按钮的资源 ID 按 AOSP 相机应用
（com.android.camera2，模拟器自带）；真机上的相机应用不同，拍照一步需要手动按快门（脚本会等待最多 60 秒）。

环境变量：BLINK_E2E_HOST_API（默认 http://127.0.0.1:8080/api/v1）、BLINK_E2E_DEVICE_API（默认 http://10.0.2.2:8080/api/v1）、BLINK_E2E_OUT。
"""
import os
import subprocess
import sys
import time
import traceback

from api import Api
from device import ADB, PKG, Device

HOST_API = os.environ.get("BLINK_E2E_HOST_API", "http://127.0.0.1:8080/api/v1")
DEVICE_API = os.environ.get("BLINK_E2E_DEVICE_API", "http://10.0.2.2:8080/api/v1")
OUT = os.environ.get("BLINK_E2E_OUT") or os.path.join(os.path.dirname(__file__), "..", "build", "e2e-image-" + time.strftime("%Y%m%d-%H%M%S"))
PASSWORD = "Passw0rd!e2e"
LAMP = os.path.join(os.path.dirname(__file__), "..", "..", "backend", "assets", "catalog", "products", "p_seed_lamp.png")

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


def tap_full_id(full, timeout=15):
    """按完整资源 ID 点其他应用（权限框、相机）里的控件。"""
    end = time.time() + timeout
    while time.time() < end:
        for n in dev.nodes():
            if n.get("resource-id") == full:
                x, y = dev.center(n)
                dev.shell(f"input tap {x} {y}")
                return
        time.sleep(1)
    raise AssertionError("not found: " + full)


def top_activity():
    out = dev.shell("dumpsys activity activities")
    for line in out.splitlines():
        if "topResumedActivity" in line:
            return line
    return ""


@step("注册并进入 AI 导购；附图按钮有内容描述")
def open_chat(username):
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
    dev.tap(rid="chat_button")
    dev.wait(text="你好，我是 Blink 导购助手")
    dev.wait(rid="attach_button", desc="添加图片：从相册选择或拍照")


@step("拍照：拒绝相机权限 → 提示可以从相册选择")
def deny_once():
    dev.shell(f"pm revoke {PKG} android.permission.CAMERA")
    dev.shell(f"pm clear-permission-flags {PKG} android.permission.CAMERA user-set user-fixed")
    dev.tap(rid="attach_button")
    dev.tap(text="拍照")
    dev.tap(text="Don’t allow")  # 系统权限框（英文系统；中文系统为“不允许”）
    dev.wait(text="没有相机权限")
    dev.wait(text="拍照找同款需要相机权限。也可以从相册选择图片。")
    assert not dev.find(text="去设置")
    dev.tap(text="取消")


@step("再次拒绝（不再询问）→ 提示去系统设置，“去设置”打开本应用的设置页")
def deny_blocked():
    dev.tap(rid="attach_button")
    dev.tap(text="拍照")
    dev.tap(text="Don’t allow")  # 系统权限框（英文系统；中文系统为“不允许”）
    dev.wait(text="相机权限已被关闭", contains=True)
    dev.tap(text="去设置")
    time.sleep(2)
    assert "com.android.settings" in top_activity(), top_activity()
    dev.back()
    dev.wait(rid="attach_button")


@step("授权后拍照：系统相机拍摄 → 预览“图片已就绪”")
def take_photo():
    dev.shell(f"pm grant {PKG} android.permission.CAMERA")
    dev.tap(rid="attach_button")
    dev.tap(text="拍照")
    try:
        tap_full_id("com.android.camera2:id/shutter_button", timeout=20)
        tap_full_id("com.android.camera2:id/done_button", timeout=20)
    except AssertionError:
        print("    非 AOSP 相机：请在设备上手动拍照并确认")
    dev.wait(rid="attachment_status", text="图片已就绪，发送时一起发给导购", timeout=60)
    dev.wait(rid="attachment_preview", desc="待发送的图片")


@step("只发图片：气泡“附带 1 张图片”；相机画面与商品都不像，只给外观相近参考或说明没找到")
def send_camera_photo():
    dev.tap(rid="send_button")
    dev.gone(rid="attachment_bar", timeout=5)
    end = time.time() + 30
    while time.time() < end:
        texts = [n.get("text", "") for n in dev.find(rid="answer_text")]
        if any("外观相近" in t or "没有找到和这张图片相似" in t or "最像的是" in t for t in texts):
            break
        time.sleep(1)
    else:
        raise AssertionError("no image answer")
    # 回答较长时用户气泡会滚出屏幕：往回滚一下再核对附图标记
    for _ in range(3):
        if dev.find(rid="user_attachment", text="附带 1 张图片"):
            break
        dev.swipe_down()
    dev.wait(rid="user_attachment", text="附带 1 张图片", timeout=5)
    dev.wait(rid="user_text", text="帮我找找图片里的同款", timeout=5)


@step("从相册选商品照片 → 移除 → 预览消失")
def pick_and_remove():
    pick_lamp()
    dev.tap(rid="attachment_remove")
    dev.gone(rid="attachment_status", timeout=5)


def pick_lamp():
    dev.tap(rid="attach_button")
    dev.tap(text="从相册选择")
    time.sleep(3)
    # 系统照片选择器（Android 13+）或文档选择器：点最新的一张
    for n in dev.nodes():
        if n.get("content-desc", "").startswith("Photo taken on") or "blink_lamp" in n.get("text", ""):
            x, y = dev.center(n)
            dev.shell(f"input tap {x} {y}")
            break
    else:
        raise AssertionError("photo not found in picker")
    dev.wait(rid="attachment_status", text="图片已就绪，发送时一起发给导购", timeout=30)


@step("重新选台灯照片并发送：“按图片找到的商品”第一件是 Blink 护眼台灯 L1")
def send_lamp():
    pick_lamp()
    dev.tap(rid="send_button")
    dev.wait(rid="answer_text", text="最像的是 Blink 护眼台灯 L1", contains=True, timeout=30)
    dev.wait(text="按图片找到的商品")
    dev.wait(text="把第一个加入购物车")


@step("追问“把第一个加入购物车”：服务端购物车里是台灯")
def add_first(token):
    dev.tap(text="把第一个加入购物车")
    dev.wait(rid="answer_text", text="已把 Blink 护眼台灯 L1 × 1 加入购物车", contains=True, timeout=30)
    cart = api.ok("GET", "/cart", token)
    assert [(i["product_id"], i["quantity"]) for i in cart["items"]] == [("p_seed_lamp", 1)], cart


def push_lamp():
    """把台灯商品图（缩小、转 JPEG）放进设备相册，作为“用户拍的照片”。"""
    tmp = os.path.join(OUT, "blink_lamp.jpg")
    subprocess.run(["sips", "-s", "format", "jpeg", "-Z", "320", LAMP, "--out", tmp], capture_output=True, check=True)
    subprocess.run([ADB, "push", tmp, "/sdcard/Pictures/blink_lamp.jpg"], capture_output=True, check=True)
    dev.shell("am broadcast -a android.intent.action.MEDIA_SCANNER_SCAN_FILE -d file:///sdcard/Pictures/blink_lamp.jpg")
    time.sleep(2)


def main():
    username = "e2eimg_" + str(int(time.time()))
    print("输出目录：", os.path.abspath(OUT))
    os.makedirs(OUT, exist_ok=True)
    dev.reset_app(DEVICE_API)
    push_lamp()
    rec = subprocess.Popen([ADB, "shell", "screenrecord", "--time-limit", "180", "--bit-rate", "4000000", "/sdcard/image_flow.mp4"])
    ok = False
    try:
        open_chat(username)
        token = api.login(username, PASSWORD)
        deny_once()
        deny_blocked()
        take_photo()
        send_camera_photo()
        pick_and_remove()
        send_lamp()
        add_first(token)
        ok = True
    except Exception:
        traceback.print_exc()
    finally:
        dev.shell("pkill -2 screenrecord")
        rec.wait(timeout=30)
        time.sleep(1)
        subprocess.run([ADB, "pull", "/sdcard/image_flow.mp4", os.path.join(OUT, "image_flow.mp4")], capture_output=True)
        dev.shell("rm -f /sdcard/Pictures/blink_lamp.jpg")
        with open(os.path.join(OUT, "report.md"), "w") as f:
            f.write(f"# Android 聊天附图 E2E\n\n- 时间：{time.strftime('%Y-%m-%d %H:%M:%S')}\n- 账号：{username}\n"
                    f"- 设备 API：{DEVICE_API}\n- 结果：{'通过' if ok else '失败'}\n\n| # | 步骤 | 结果 | 用时 |\n| --- | --- | --- | --- |\n")
            f.write("\n".join(report) + "\n")
    print("结果：", "通过" if ok else "失败", "；报告：", os.path.join(os.path.abspath(OUT), "report.md"))
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
