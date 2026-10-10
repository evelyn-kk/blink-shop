#!/usr/bin/env python3
"""
Android 语音 E2E（8.4），在已安装 Debug 包的模拟器或真机上运行：

  注册 → 进入 AI 导购（麦克风按钮按服务端能力显示）→ 首次点麦克风先看到隐私说明 → 拒绝录音权限（提示可以打字）→
  再次拒绝即“不再询问”（提示去设置，“去设置”打开本应用设置页）→ 授权后语音输入：状态“正在听”、转写实时写进输入框 →
  再点麦克风结束，拿到最终结果（服务端会话 outcome=final）→ 再开一次后“取消”恢复原来的输入 → 发送 → 回答下方“朗读”→
  “停止朗读”→ 再朗读时回桌面（离开页面自动停止），回来按钮恢复“朗读” → 设置里关闭语音功能后聊天里不再显示麦克风和朗读按钮。

后端要开通语音（测试库）：STT_PROVIDER=mock TTS_PROVIDER=mock（mock 不调外部服务，识别结果固定为“推荐一款降噪耳机”）。
模拟器的虚拟麦克风会很快送完一段静音然后停住，所以开始后几秒内就结束输入；真机上没有这个问题。
环境变量：BLINK_E2E_HOST_API、BLINK_E2E_DEVICE_API、BLINK_E2E_OUT（同 image_flow.py）。
"""
import os
import subprocess
import sys
import time
import traceback

from device import ADB, PKG
import image_flow as base

dev, api, step = base.dev, base.api, base.step
TEXT = "推荐一款降噪耳机"


def input_text():
    nodes = dev.find(rid="input")
    return nodes[0].get("text", "") if nodes else ""


@step("首次点麦克风：先说明语音怎么用（隐私说明），拒绝录音权限后提示可以打字")
def deny_once():
    dev.shell(f"pm revoke {PKG} android.permission.RECORD_AUDIO")
    dev.shell(f"pm clear-permission-flags {PKG} android.permission.RECORD_AUDIO user-set user-fixed")
    dev.wait(rid="mic_button", desc="语音输入")
    dev.tap(rid="mic_button")
    dev.wait(text="使用语音输入")
    dev.wait(text="不保存录音和文字原文", contains=True)
    dev.tap(text="继续")
    dev.tap(text="Don’t allow")
    dev.wait(text="没有录音权限")
    dev.wait(text="不授权也可以直接打字提问", contains=True)
    assert not dev.find(text="去设置")
    dev.tap(text="知道了")


@step("再次拒绝（不再询问）：提示去系统设置，“去设置”打开本应用设置页")
def deny_blocked():
    dev.tap(rid="mic_button")
    dev.tap(text="继续")
    dev.tap(text="Don’t allow")
    dev.wait(text="录音权限已被关闭", contains=True)
    dev.tap(text="去设置")
    time.sleep(2)
    assert "com.android.settings" in base.top_activity(), base.top_activity()
    dev.back()
    dev.wait(rid="mic_button")


@step("授权后语音输入：正在听 → 转写写进输入框 → 再点麦克风结束，得到最终结果")
def speak():
    dev.shell(f"pm grant {PKG} android.permission.RECORD_AUDIO")
    dev.tap(rid="mic_button")  # 已有权限：直接开始，不再弹说明
    dev.wait(rid="voice_status", text="正在听…说完再点一次麦克风", timeout=15)
    dev.wait(rid="mic_button", desc="结束语音输入")
    end = time.time() + 10
    while time.time() < end and not input_text():
        time.sleep(0.5)
    assert input_text() and TEXT.startswith(input_text().strip()), input_text()
    dev.tap(rid="mic_button")
    dev.gone(rid="voice_status", timeout=20)
    assert input_text() == TEXT, input_text()


@step("再开一次语音输入后“取消”：输入框恢复原来的文字，不追加转写")
def cancel_voice():
    before = input_text()
    dev.tap(rid="mic_button")
    dev.wait(rid="voice_status", text="正在听…说完再点一次麦克风", timeout=15)
    dev.tap(rid="voice_cancel")
    dev.gone(rid="voice_status", timeout=10)
    assert input_text() == before, input_text()


@step("发送语音输入的问题：回答下方有“朗读”；点击后变“停止朗读”，再点停止")
def tts_toggle():
    dev.tap(rid="send_button")
    dev.wait(rid="speak_button", text="朗读", timeout=30)
    dev.wait(rid="speak_button", desc="朗读这段回答")
    dev.tap(rid="speak_button", text="朗读")
    dev.wait(rid="speak_button", text="停止朗读", timeout=15)
    dev.wait(rid="speak_button", desc="停止朗读这段回答")
    dev.tap(rid="speak_button", text="停止朗读")
    dev.wait(rid="speak_button", text="朗读", timeout=10)


@step("朗读中回桌面：离开页面自动停止；回来按钮恢复“朗读”，App 没有崩溃")
def tts_background():
    dev.shell("logcat -c")
    dev.tap(rid="speak_button", text="朗读")
    dev.wait(rid="speak_button", text="停止朗读", timeout=15)
    dev.shell("input keyevent 3")
    time.sleep(2)
    dev.shell(f"am start -n {PKG}/.catalog.ProductListActivity")
    time.sleep(2)
    dev.wait(rid="speak_button", text="朗读", timeout=10)
    crash = dev.shell(f"logcat -d -b crash")
    assert PKG not in crash, crash


def open_voice_setting():
    """首页 → 账户 → 设置与帮助 → 点“语音输入与朗读”切换。"""
    dev.tap(rid="account_button")
    for _ in range(3):
        if dev.find(rid="row_settings"):
            break
        dev.swipe_up()
    dev.tap(rid="row_settings")
    dev.tap(text="语音输入与朗读")


@step("设置里关闭语音功能：聊天里不再显示麦克风和朗读按钮；重新开启后恢复")
def toggle_off():
    dev.back()
    dev.wait(rid="account_button")
    open_voice_setting()
    dev.wait(text="已关闭：聊天里不显示语音按钮")
    dev.back()
    dev.back()
    dev.tap(rid="chat_button")
    dev.wait(rid="input")
    time.sleep(2)
    assert not dev.find(rid="mic_button") and not dev.find(rid="speak_button")
    dev.back()
    open_voice_setting()
    dev.wait(text="已开启", contains=True)
    dev.back()
    dev.back()


def main():
    username = "e2evoice_" + str(int(time.time()))
    print("输出目录：", os.path.abspath(base.OUT))
    os.makedirs(base.OUT, exist_ok=True)
    dev.reset_app(base.DEVICE_API)
    rec = subprocess.Popen([ADB, "shell", "screenrecord", "--time-limit", "180", "--bit-rate", "4000000", "/sdcard/voice_flow.mp4"])
    ok = False
    try:
        base.open_chat(username)
        deny_once()
        deny_blocked()
        speak()
        cancel_voice()
        tts_toggle()
        tts_background()
        toggle_off()
        ok = True
    except Exception:
        traceback.print_exc()
    finally:
        dev.shell("pkill -2 screenrecord")
        rec.wait(timeout=30)
        time.sleep(1)
        subprocess.run([ADB, "pull", "/sdcard/voice_flow.mp4", os.path.join(base.OUT, "voice_flow.mp4")], capture_output=True)
        with open(os.path.join(base.OUT, "report.md"), "w") as f:
            f.write(f"# Android 语音 E2E\n\n- 时间：{time.strftime('%Y-%m-%d %H:%M:%S')}\n- 账号：{username}\n"
                    f"- 设备 API：{base.DEVICE_API}\n- 结果：{'通过' if ok else '失败'}\n\n| # | 步骤 | 结果 | 用时 |\n| --- | --- | --- | --- |\n")
            f.write("\n".join(base.report) + "\n")
    print("结果：", "通过" if ok else "失败", "；报告：", os.path.join(os.path.abspath(base.OUT), "report.md"))
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
