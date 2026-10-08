# Android 交易闭环 E2E

`trade_flow.py` 用 adb + uiautomator 在模拟器或真机上操作 Debug 包，覆盖 docs/07 第 1 条（注册 → 登录 → 筛选 → 详情 →
加购 → 优惠试算 → 下单 → 模拟支付 → 商家发货 → 收货 → 评价）和 6.2 的测试点：空购物车、库存变化、断网提交后冻结上次提交（不能换券、原样重发）、连点提交只下一单、
断网支付失败后重新支付、App 在后台被杀后订单重查、评价草稿恢复、取消订单（覆盖全部订单状态）、领券和帮助页。
商家发货和结果核对直接调 API。只依赖 Python 3 标准库和 adb。

## 运行

1. 起一个**测试用**的 Blink API（脚本会注册账号、下单、改测试商品库存），例如独立临时库、端口 28080：

   ```bash
   cd backend
   MYSQL_DSN='root:blink_dev_root@tcp(127.0.0.1:3307)/blink_shop_e2e' go run ./cmd/seed
   MYSQL_DSN='root:blink_dev_root@tcp(127.0.0.1:3307)/blink_shop_e2e' API_ADDR=:28080 \
     RATE_LIMIT_IP_PER_MINUTE=6000 RATE_LIMIT_ACCOUNT_PER_MINUTE=6000 go run ./cmd/api
   ```

2. 安装 Debug 包：`./gradlew assembleDebug && adb install -r app/build/outputs/apk/debug/app-debug.apk`。

3. 运行（多台设备时用 `ANDROID_SERIAL` 指定）：

   ```bash
   cd android-native
   python3 e2e/trade_flow.py                                   # 模拟器，App 访问 http://10.0.2.2:28080/api/v1
   adb reverse tcp:28080 tcp:28080 && \
     BLINK_E2E_DEVICE_API=http://127.0.0.1:28080/api/v1 python3 e2e/trade_flow.py   # 真机经 USB
   ```

脚本开始时会清空 App 数据并写入服务地址，把测试商品（耳机、台灯）的库存设为固定值；每一步截图，
报告和截图写到 `build/e2e-<时间>/`（`BLINK_E2E_OUT` 可改）。结束时打开设备网络。

## 注意

- 只连测试库：脚本会注册 `e2e_*` 账号并产生订单、评价。
- adb 只能输入英文，评价内容用英文；中文输入由单测覆盖。
- 服务端会自动关闭 30 分钟未支付的订单并回补库存，所以“库存变化”一步以服务端当时的判断为准。
