"""从电脑这一侧直接调 Blink API：准备数据、扮演商家发货、核对服务端状态。"""
import json
import urllib.error
import urllib.request


class Api:
    def __init__(self, base):
        self.base = base.rstrip("/")

    def call(self, method, path, token=None, body=None):
        data = None if body is None else json.dumps(body).encode()
        req = urllib.request.Request(self.base + path, data=data, method=method)
        req.add_header("Accept", "application/json")
        if data is not None:
            req.add_header("Content-Type", "application/json")
        if token:
            req.add_header("Authorization", "Bearer " + token)
        try:
            with urllib.request.urlopen(req, timeout=15) as r:
                text = r.read().decode()
                return r.status, json.loads(text) if text else {}
        except urllib.error.HTTPError as e:
            text = e.read().decode()
            return e.code, json.loads(text) if text else {}

    def ok(self, method, path, token=None, body=None):
        status, data = self.call(method, path, token, body)
        if status // 100 != 2:
            raise AssertionError(f"{method} {path} -> {status} {data}")
        return data

    def login(self, username, password):
        return self.ok("POST", "/auth/login", body={"username": username, "password": password})["token"]
