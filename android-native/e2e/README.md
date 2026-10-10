# Android 端到端脚本

## 交易闭环 `trade_flow.py`

`trade_flow.py` 用 adb + uiautomator 在模拟器或真机上操作 Debug 包，覆盖 docs/07 第 1 条（注册 → 登录 → 筛选 → 详情 →
加购 → 优惠试算 → 下单 → 模拟支付 → 商家发货 → 收货 → 评价）和 6.2 的测试点：空购物车、库存变化、断网提交后冻结上次提交（不能换券、原样重发）、首单已在服务端成功但 App 没收到响应并被杀后从购物车横幅恢复同一组订单、连点提交只下一单、
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

## 聊天页 `chat_flow.py`

覆盖 7.3 的聊天体验：注册 → 首页“导购”进聊天页 → 预设问题“推荐一款通勤降噪耳机”（思考步骤、流式正文、商品卡、追问）→
追问“把第一个加入购物车”“去结算”“支付订单”（每步用 API 核对购物车和订单）→ 订单卡点开详情 → 新对话、空购物车、优惠券、领券 →
断网发送显示“网络未连接”+“重试”，恢复后重试成功 → 发送后立刻回桌面再回来内容完整 → 后台被杀后重进按会话 ID 从服务端恢复、消息数不变 →
历史抽屉（列表、置顶、重命名、切换会话、删除）。全程录屏 `chat_flow.mp4`，每步截图和 `report.md` 写到输出目录。

```bash
# 测试用 API 跑在 8080（模拟器默认地址 http://10.0.2.2:8080/api/v1），或用 BLINK_E2E_HOST_API / BLINK_E2E_DEVICE_API 指定
cd android-native && python3 e2e/chat_flow.py
```

“停止生成”在本地规则运行器下回答只要一两百毫秒，模拟器上来不及点，由单测覆盖（`ChatControllerTest`、`ChatStreamTransportTest`）。

## 聊天附图 `image_flow.py`

覆盖 8.3 的附图：拍照时拒绝相机权限（提示改用相册）→ 再次拒绝即“不再询问”（提示去系统设置，“去设置”打开本应用设置页）→
授权后用系统相机拍照 → 预览“图片已就绪” → 只发图片（气泡“附带 1 张图片”，模拟器相机画面和商品都不像，只给外观相近参考）→
从相册选台灯照片（脚本先把商品图推进设备相册）→ 移除 → 重新选择并发送 →“按图片找到的商品”第一件是台灯 → 追问加购，API 核对购物车。

后端需要配置图片搜索和对象存储（测试库 + 临时集合）：

```bash
cd backend
MYSQL_DSN='root:blink_dev_root@tcp(127.0.0.1:3307)/blink_shop_e2e?parseTime=true' go run ./cmd/seed
MYSQL_DSN='root:blink_dev_root@tcp(127.0.0.1:3307)/blink_shop_e2e?parseTime=true' API_ADDR=:8080 \
  MINIO_ENDPOINT=127.0.0.1:9000 MINIO_ACCESS_KEY=minioadmin MINIO_SECRET_KEY=minioadmin \
  MILVUS_ADDR=127.0.0.1:19530 MILVUS_IMAGE_COLLECTION=blink_shop_e2e_images \
  RATE_LIMIT_IP_PER_MINUTE=6000 RATE_LIMIT_ACCOUNT_PER_MINUTE=6000 go run ./cmd/api
cd ../android-native && python3 e2e/image_flow.py
```

快门和确认按钮按 AOSP 相机（模拟器自带的 `com.android.camera2`）定位；真机的相机应用不同，脚本会等最多 60 秒让人手动拍照。
权限框按英文系统的按钮文字（“Don’t allow”）点击。用完删掉临时集合（`blink_shop_e2e_images`）。

## 语音 `voice_flow.py`

覆盖 8.4：首次点麦克风先看隐私说明 → 拒绝录音权限（提示可以打字）→ 再次拒绝即“不再询问”（提示去设置，“去设置”打开本应用设置页）→
授权后语音输入（正在听、转写实时写进输入框、再点麦克风得到最终结果）→ 再开一次后“取消”还原输入 → 发送 → “朗读 / 停止朗读”→
朗读中回桌面（离开页面自动停止，回来不崩溃）→ 设置里关闭语音功能后不再显示麦克风和朗读按钮。

后端开 mock 语音（测试库）：`STT_PROVIDER=mock TTS_PROVIDER=mock`，mock 识别结果固定为“推荐一款降噪耳机”。

```bash
cd android-native && python3 e2e/voice_flow.py
```

模拟器的虚拟麦克风在 `-no-audio` 下也能出数据。权限框按英文系统的按钮文字点击。
