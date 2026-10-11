#!/usr/bin/env python3
"""语音服务未配置（生产部署 STT/TTS off）时 App 的表现：聊天页不显示麦克风和朗读按钮，文字导购照常，App 不崩溃。

环境变量同 image_flow.py（BLINK_E2E_HOST_API、BLINK_E2E_DEVICE_API、BLINK_E2E_OUT）。
"""
import os
import sys
import time
import traceback

import image_flow as base


def main():
    dev = base.dev
    username = "e2eoff_" + str(int(time.time()))
    os.makedirs(base.OUT, exist_ok=True)
    dev.reset_app(base.DEVICE_API)
    ok = False
    try:
        base.open_chat(username)
        assert not dev.find(rid="mic_button"), "语音未配置时不应显示麦克风"
        dev.tap(text="看看我的购物车")  # 预设问题
        dev.wait(contains=True, text="还是空的", timeout=30)  # 有回答
        time.sleep(2)
        assert not dev.find(text="朗读"), "语音未配置时不应显示朗读"
        assert "com.blink.shop/" in base.top_activity(), "App 不应崩溃或退出"
        dev.shot("speech_off")
        ok = True
        print("[01] 通过：语音未配置：没有麦克风和朗读按钮，文字导购正常，App 没有崩溃")
    except Exception:
        traceback.print_exc()
        dev.shot("speech_off_FAILED")
    print("结果：", "通过" if ok else "失败")
    sys.exit(0 if ok else 1)


if __name__ == "__main__":
    main()
