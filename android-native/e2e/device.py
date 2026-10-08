"""用 adb + uiautomator 驱动 App 的小工具（只用标准库）。"""
import os
import re
import subprocess
import time
import xml.etree.ElementTree as ET

PKG = "com.blink.shop"
ADB = os.environ.get("ADB") or os.path.expanduser("~/Library/Android/sdk/platform-tools/adb")


class Device:
    def __init__(self, shots_dir):
        self.shots_dir = shots_dir
        os.makedirs(shots_dir, exist_ok=True)

    # ---------- adb ----------

    def adb(self, *args, check=False):
        r = subprocess.run([ADB, *args], capture_output=True, text=True)
        if check and r.returncode != 0:
            raise RuntimeError(f"adb {' '.join(args)}: {r.stderr.strip()}")
        return r.stdout

    def shell(self, cmd):
        return self.adb("shell", cmd)

    # ---------- 界面树 ----------

    def nodes(self):
        for _ in range(5):
            self.shell("uiautomator dump /sdcard/blink_e2e_ui.xml >/dev/null 2>&1")
            xml = self.adb("exec-out", "cat", "/sdcard/blink_e2e_ui.xml")
            if xml.strip().startswith("<?xml"):
                return list(ET.fromstring(xml).iter("node"))
            time.sleep(0.5)
        raise RuntimeError("uiautomator dump failed")

    @staticmethod
    def center(n):
        x1, y1, x2, y2 = map(int, re.findall(r"\d+", n.get("bounds")))
        return (x1 + x2) // 2, (y1 + y2) // 2

    def find(self, text=None, rid=None, desc=None, contains=False):
        out = []
        for n in self.nodes():
            if rid and n.get("resource-id") != f"{PKG}:id/{rid}":
                continue
            if text is not None:
                t = n.get("text", "")
                if not (text in t if contains else t == text):
                    continue
            if desc is not None:
                d = n.get("content-desc", "")
                if not (desc in d if contains else d == desc):
                    continue
            out.append(n)
        return out

    def wait(self, timeout=15, **kw):
        end = time.time() + timeout
        while time.time() < end:
            hits = self.find(**kw)
            if hits:
                return hits
            time.sleep(0.5)
        raise AssertionError(f"没有等到界面元素 {kw}")

    def gone(self, timeout=15, **kw):
        end = time.time() + timeout
        while time.time() < end:
            if not self.find(**kw):
                return
            time.sleep(0.5)
        raise AssertionError(f"界面元素没有消失 {kw}")

    def text_of(self, rid):
        hits = self.wait(rid=rid)
        return hits[0].get("text", "")

    def tap(self, nth=0, timeout=15, **kw):
        hits = self.wait(timeout=timeout, **kw)
        x, y = self.center(hits[nth])
        self.shell(f"input tap {x} {y}")
        time.sleep(0.4)

    def double_tap_fast(self, **kw):
        """几乎同时点两次（测试重复提交）。"""
        x, y = self.center(self.wait(**kw)[0])
        self.shell(f"input tap {x} {y} & input tap {x} {y}")
        time.sleep(0.4)

    def type(self, text):
        """只能输入 ASCII（adb 限制）。"""
        self.shell("input text " + text.replace(" ", "%s").replace("'", "").replace("&", ""))

    def clear_field(self, n=40):
        self.shell("input keyevent 123")
        self.shell("input keyevent " + " ".join(["67"] * n))

    def back(self):
        self.shell("input keyevent 4")
        time.sleep(0.6)

    def hide_keyboard(self):
        self.shell("input keyevent 111")
        time.sleep(0.3)

    def swipe_up(self):
        self.shell("input swipe 540 1800 540 700 300")
        time.sleep(0.6)

    def swipe_down(self):
        """下拉刷新（慢一点拖，避免被当成普通滚动）。"""
        self.shell("input swipe 540 600 540 1700 700")
        time.sleep(1.2)

    def shot(self, name):
        path = os.path.join(self.shots_dir, name + ".png")
        with open(path, "wb") as f:
            f.write(subprocess.run([ADB, "exec-out", "screencap", "-p"], capture_output=True).stdout)
        return path

    # ---------- App ----------

    def reset_app(self, api_base):
        """清空 App 数据，并在首次启动前写入服务地址（只对 Debug 包有效）。"""
        self.adb("shell", "pm", "clear", PKG, check=True)
        prefs = ("<?xml version='1.0' encoding='utf-8' standalone='yes' ?>\n<map>\n"
                 f'    <string name="api_base">{api_base}</string>\n</map>\n')
        subprocess.run([ADB, "shell", f"run-as {PKG} sh -c 'mkdir -p shared_prefs && cat > shared_prefs/blink_settings.xml'"],
                       input=prefs, text=True, check=True)

    def write_pending_checkout(self, account_id, pending):
        """在 App 停止时写入一条“结果未确认的结算提交”（模拟提交后没收到响应就被杀）。"""
        import json
        from xml.sax.saxutils import escape
        self.shell(f"am force-stop {PKG}")
        value = escape(json.dumps(pending), {'"': "&quot;"})
        xml = ("<?xml version='1.0' encoding='utf-8' standalone='yes' ?>\n<map>\n"
               f'    <string name="checkout_pending.{account_id}">{value}</string>\n</map>\n')
        subprocess.run([ADB, "shell", f"run-as {PKG} sh -c 'mkdir -p shared_prefs && cat > shared_prefs/blink_drafts.xml'"],
                       input=xml, text=True, check=True)

    def start(self):
        self.shell(f"am start -W -n {PKG}/.catalog.ProductListActivity")
        time.sleep(1)

    def restart(self):
        """结束进程后从首页重新启动（不恢复之前的页面）。"""
        self.shell(f"am start -W -S -n {PKG}/.catalog.ProductListActivity")
        time.sleep(1)

    def kill(self):
        """模拟 App 在后台被系统杀死（系统会保留页面栈，重进时恢复）。返回 True 表示是这种“后台被杀”；
        如果几秒内杀不掉，只能强制停止，此时页面栈不保留，返回 False。"""
        self.shell("input keyevent 3")
        end = time.time() + 8
        while time.time() < end:
            time.sleep(1)
            self.shell(f"am kill {PKG}")
            if not self.shell(f"pidof {PKG}").strip():
                return True
        self.shell(f"am force-stop {PKG}")
        return False

    def set_network(self, on):
        if on:
            self.shell("cmd wifi set-wifi-enabled enabled")
            self.shell("svc data enable")
        else:
            self.shell("cmd wifi set-wifi-enabled disabled")
            self.shell("svc data disable")
        time.sleep(4)
