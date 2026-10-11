# 最终验收记录（10.2）

验收对象：v1.0.0 候选（tag `v1.0.0-rc.1`）。日期 2026-10-11。执行人：开发者（claude）；审查与签核：审查者（codex）、项目负责人（待签）。
复测方法见 `quality/README.md`“acceptance/”，各条的详细输出（截图、报告）保存在本地台账目录，不进仓库。
第 1–6 节 Android 部分在模拟器上完成；同日在真机（Pixel 6a，Android 16）上补测，见第 7 节。

## 1. 验收环境（部署拓扑）

```text
浏览器 / Android ─▶ web（nginx 1.29.2：静态站点 + /api 反代，SSE 不缓冲、WebSocket 升级、上游连接池 + DNS 动态解析）
                     └▶ api（blink-shop-api 镜像，APP_ENV=production，所有生产校验生效）
                          ├▶ MySQL 8.4（专用库 blink_shop_accept、专用账号、随机强密码；/migrate 执行迁移）
                          ├▶ MinIO（专用账号与桶）
                          └▶ Milvus 2.5（临时图片集合，本地图片特征）
```

- 真机经 Wi-Fi 访问：宿主机上 `quality/acceptance/lan_forward.py` 把局域网地址转发到只绑本机的 web 端口（见第 7 节），不用 `adb reverse`。
- 搭建 / 清理：`quality/acceptance/setup.sh` / `teardown.sh`（应用层 `deployments/app/` 接入本地基础设施网络）。没有配置 Nacos（生产要求开鉴权，本地开发实例没开），动态配置在内存。
- 附加实例（同一镜像、同一验收库）：
  - **降级实例**：不配 MinIO、Milvus，本来也没有模型，用于第 4 条；
  - **慢模型实例**：模型指向 `quality/acceptance/fakellm`，用于第 3 条取消和第 7 条；
  - **语音实例**：开发模式 + mock 语音，因为生产拒绝 mock，用于语音脚本；
  - **空库实例**：只执行迁移、没有种子数据。
- 冻结指纹：`backend/openapi.yaml` sha256 `70e445f0a19c1a0c8cc3b38e5a2eba9237eda4609bf1e999daf7c86c0e7d775c`（info.version 1.0.0，103 个操作）；
  `backend/migrations/checksums.txt` sha256 `fcbdfc080915882e237b64a78d6f39800c77a91ba15119c96b4a3286157f557c`（0001–0008）。

## 2. docs/07 八条 E2E 验收

| # | 验收条目 | 执行方式 | 结果 |
| --- | --- | --- | --- |
| 1 | 注册→登录→筛选→详情→加购→试算→下单→支付→商家发货→收货→评价 | Android `trade_flow.py`（模拟器与真机，经 nginx）16 步；Web E2E“下单 → 支付 → 商家在 Web 发货 → 买家确认收货 → 评价” | 通过（模拟器、真机均 16/16；Web 四种浏览器） |
| 2 | 并发两次 checkout 只生成一组订单、库存不为负；库存不足无半成品订单 | `check.py` 第 2 条；`cmd/loadtest` 抢购（20 人抢 5 件）与重复提交（10 个购物车）经 nginx | 通过 |
| 3 | 相同 client_message_id 重发不产生第二条消息 / 第二次运行；取消 SSE 后运行 cancelled | `check.py` 第 3 条（慢模型实例上运行中取消，0.8s 结束为 cancelled） | 通过 |
| 4 | 无 AI / Milvus / MinIO：交易可用、导购说明降级且不虚构引用、图搜 / 上传规范错误 | `check.py` 第 4 条（降级实例） | 通过（6 项） |
| 5 | 用户间文件 / 会话 / 购物车 / 订单隔离；商家间商品隔离；普通用户不能访问 /admin | `check.py` 第 5 条 + Web E2E 越权用例 | 通过（6 项） |
| 6 | 伪造 MIME、超限文件、恶意 URL、非法订单状态迁移、过期券、重复评价被拒且错误码稳定 | `check.py` 第 6 条（错误码：unsupported_file_type、payload_too_large、url_not_allowed、order_status_conflict、coupon_unavailable、review_exists） | 通过（6 项） |
| 7 | 发布 Prompt / 配置后运行 trace 关联生效版本；secret 掩码 | `check.py` 第 7 条（慢模型实例：发布 v3 后轨迹和运行记录都是 v3；配置改动立即生效；13 项密钥全部掩码）+ Web E2E Prompt / 配置用例 | 通过 |
| 8 | Android 冷启动完成导购图文流式、图片附件、购物车与订单；断网 / 拒权 / 服务未配置可恢复 | Android `chat_flow.py` 10 步、`image_flow.py` 8 步、`voice_flow.py` 8 步、`speech_off_check.py`（生产部署语音未配置）；真机上前三个脚本再跑一遍 | 通过（模拟器、真机） |

`check.py` 最终一轮：25 项全部通过（第 2–7 条）。

## 3. 浏览器、空数据与网络异常

| 项目 | 结果 |
| --- | --- |
| Web E2E 对生产构建（nginx）：桌面 Chromium、窄屏 Chromium（375px）、Firefox、WebKit（Safari 内核） | 176/176 通过 |
| `files.spec`（需要 Vite 开发服务器导入源码模块，Vite 代理到同一部署），四种浏览器 | 12/12 通过 |
| 空数据（只迁移、无种子）：商品列表 / 搜索 / 不存在的商品显示空态，新用户可注册，购物车 / 订单 / 会话为空，前端无报错（`empty-data.mjs`） | 7/7 通过 |
| 网络异常：Android 断网提交冻结与原样重发、断网支付失败后重付、聊天断网“重试”、后台被杀恢复；Web 接口失败重试 | 通过 |
| 拓扑压测（经 nginx，限流放宽）：目录 P95 9.0ms（约 6880 次/秒）、普通接口 P95 11.0ms、200 条 SSE 首个事件 P95 147ms、50 人结算 P95 117ms、抢购与重复提交正确；nginx 错误 0 | 达标 |

## 4. 验收中发现并修复的问题

| 问题 | 影响 | 修复 |
| --- | --- | --- |
| 镜像 ENTRYPOINT 是 `/api`，`docker run <镜像> /migrate -apply` 会把参数交给 /api 并**启动 API 而不是迁移**（10.1 运维手册的写法） | 发布时以为在迁移，实际起了一个服务 | `/api` 遇到不认识的参数报错退出；手册、Dockerfile、脚本改为 `--entrypoint /migrate` |
| nginx 的 server 级安全头被 /api/ 继承，和后端的叠加（`nosniff, nosniff`、两份 CSP） | 文件下载等响应头重复 | 安全头只加在静态页面 location |
| nginx 只在启动时解析 `api`，API 容器重建后 IP 变了仍转发到旧地址（502） | 滚动发布 / 重启 API 后全部请求失败，直到手工重载 nginx | upstream `server api:8080 resolve` + Docker DNS，验证 API 换 IP（.8 → .13）后 5 秒内自动恢复 |
| nginx 到 API 没有连接复用，高并发时本地端口被 TIME_WAIT 占满（`connect() failed (99)`，目录压测 17% 错误） | 高峰期 502 | upstream `keepalive 64`；之后压测 0 错误、TIME_WAIT 478 |
| `agent_runs.prompt_versions_json` 从未写入，调试观测的“Prompt 版本”一直为空 | 第 7 条“运行关联生效版本”只能翻轨迹 | 运行结束时写入实际用到的 Prompt 与版本（`TestPromptVersionInRunTraces` 增加断言） |
| Android SSE 读超时 120 秒：网络切换后连接失效、回答丢失时，“正在思考”要等 2 分钟才报错（验收中出现过一次，重跑 3 次都通过） | 体验差 | 读超时改为 45 秒（服务端每 15 秒发心跳） |
| 容器存活检查：distroless 镜像没有 curl | Compose / 编排没有可用的存活检查 | `/api -healthcheck` 请求本机 /api/v1/health |

脚本侧的修正：Android 恢复网络后改为等设备真的连通（原来固定等 4 秒，宿主机负载高时第一次请求会超时）；验收检查按接口真实格式和种子数据调整。

## 5. 未覆盖 / 待补

- ~~Android 真机~~：已补测，见第 7 节。只测了一台（Pixel 6a / Android 16）；其他厂商系统（权限框、相机、照片选择器的界面不同）没有覆盖。
- 语音在候选部署上是关闭的（没有语音供应商密钥，生产模式拒绝 mock）；真机语音链路在开发模式 + mock 语音的附加实例上验证（第 7 节），接入真实供应商后需要在真机上复测 `voice_flow.py`。
- 真实模型、Embedding、语音供应商在验收中用假服务 / mock 代替；接入真实供应商后按运维手册复测第 3、7、8 条。
- Nacos 在验收部署里没有接（生产要求开鉴权）；Nacos 的行为由 9.1 的集成测试与 10.1 演练覆盖。

## 6. 发布检查清单（签核）

| 项目 | 状态 | 证据 |
| --- | --- | --- |
| CI 全绿（6 个任务） | 完成 | 见版本提交的 GitHub Actions |
| 第 07 章八条 E2E | 完成（Android 模拟器 + 真机） | 第 2、7 节 |
| 主流桌面浏览器、窄屏、空数据、网络异常 | 完成 | 第 3 节 |
| 演示数据无敏感信息（只有 400 客服电话、无真实个人信息；种子在生产模式拒绝运行） | 完成 | `backend/src/seed` |
| API 与迁移冻结 | 完成 | 第 1 节指纹；`TestMigrationsFrozen` |
| 版本说明 | 完成 | `docs/release-notes/v1.0.0.md` |
| 版本 tag 与回滚点 | 候选 `v1.0.0-rc.1` 已打；回滚点 `c88bec9`（10.1 最终提交，数据库结构相同，可直接回滚镜像） | 正式 `v1.0.0` 在签核后打 |
| Android 真机验收 | 完成（Pixel 6a / Android 16，经 Wi-Fi） | 第 7 节 |
| 审查者签核 | 待审查 | |
| 项目负责人签核 | 待签 | |

## 7. Android 真机验收

| 项目 | 内容 |
| --- | --- |
| 设备 | Google Pixel 6a，Android 16（API 36），系统版本 `CP1A.260405.005`，安全补丁 2026-04-05；无 SIM 卡，系统语言中文，输入法 Gboard |
| App | `com.blink.shop` 1.0.0（versionCode 1）Debug 包，即 `v1.0.0-rc.1`（`4925b5d`）的 App 源码；手机上的 APK 与该源码重新构建的包除签名外逐文件一致 |
| 服务端 | 第 1 节的候选部署（API 生产模式 + nginx），web 端口 18088 只绑本机 |
| 网络 | 手机和宿主机在同一 Wi-Fi；宿主机 `lan_forward.py 192.168.8.107:18188 127.0.0.1:18088`，App 服务地址 `http://192.168.8.107:18188/api/v1`；**没有用 `adb reverse`**，断网即关闭手机 Wi-Fi（无 SIM，没有蜂窝兜底），恢复后脚本 ping 宿主机直到连通 |
| 日期 | 2026-10-11 16:48–17:21 |

运行命令（`android-native/` 下；语音见下）：

```bash
export ANDROID_SERIAL=<序列号> BLINK_E2E_HOST_API=http://127.0.0.1:18088/api/v1 \
  BLINK_E2E_DEVICE_API=http://192.168.8.107:18188/api/v1 BLINK_E2E_PING_HOST=192.168.8.107
BLINK_E2E_OUT=build/real-trade python3 e2e/trade_flow.py
BLINK_E2E_OUT=build/real-chat  python3 e2e/chat_flow.py
BLINK_E2E_OUT=build/real-image python3 e2e/image_flow.py
```

| 脚本 | 结果 | 真实网络异常 / 恢复 |
| --- | --- | --- |
| `trade_flow.py` | 16/16 通过 | 第 9 步断网提交：结果未知、冻结上次提交；第 10 步恢复后连点两次重发只生成一组订单（2 个）；第 11 步断网支付失败、恢复后重付成功；第 12、14、16 步后台被杀后恢复 |
| `chat_flow.py` | 10/10 通过 | 第 7 步断网发送显示“网络未连接”和“重试”，恢复后重试成功；第 8 步回桌面后回答完整；第 9 步被杀后按会话 ID 恢复、不重复发送 |
| `image_flow.py` | 8/8 通过 | 两次拒绝相机权限（第二次“不再询问”→ 去设置）；Google 相机拍照；系统照片选择器选台灯图，图搜第一件是台灯，追问加购 |
| `voice_flow.py` | 8/8 通过 | 两次拒绝录音权限；授权后用手机麦克风录音，音频经 WebSocket 送到服务端识别，转写写进输入框；取消、朗读 / 停止朗读、离开页面停止、设置里关闭语音 |

语音：候选部署没有语音供应商（生产模式拒绝 mock，语音按“未配置”隐藏，与模拟器上 `speech_off_check.py` 的结论一致）。
为了在真机上走一遍录音 → 实时识别 → 朗读的完整客户端链路，另起一个附加实例：同一份代码、同一验收库，开发模式 + `STT_PROVIDER=mock TTS_PROVIDER=mock`，
本机 18090，经 `lan_forward.py 192.168.8.107:18190 127.0.0.1:18090` 给手机访问（`BLINK_E2E_HOST_API=http://127.0.0.1:18090/api/v1 BLINK_E2E_DEVICE_API=http://192.168.8.107:18190/api/v1`）。
mock 识别按收到的音频时长逐字吐出固定文字，所以转写出现说明真机麦克风的音频确实送到了服务端；识别准确率要等接入真实供应商后复测。

失败与重试（都是测试脚本对真机的适配，App 和服务端没有改动）：

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `trade_flow` 第 1 步截图全黑、找不到控件 | 手机熄屏锁屏；`svc power stayon usb` 不生效（手机报告为交流供电） | 测试期间 `svc power stayon true`（结束后恢复为 false）；脚本启动 / 重启 App 前先点亮屏幕并解除无密码锁屏（`device.wake()`） |
| `trade_flow` 第 14 步评价内容只有 “Quiet and” | Gboard 拼音模式把 adb 输入的英文当拼音组字，没上屏 | 测试期间把键盘切到英文（README 注明） |
| `voice_flow` 第 2 步找不到 “Don’t allow” | 中文系统的权限框是“不允许”；第二次弹框的拒绝按钮是另一个 id | 按权限控制器的 resource-id 点击（`device.deny_permission()`），两种 id 都认 |
| `image_flow` 第 3 步打开了相机 | 第二次权限框被人工误点了“允许” | 重跑 |
| `image_flow` 第 4 步等不到“图片已就绪” | Pixel 用 Google 相机，原脚本只认 AOSP 相机 | `shoot()` 同时支持 Google 相机（拍完快门键变“完成”） |
| `image_flow` 第 6 步选择器“尚无照片” / 找不到照片 | 上一轮结束只 `rm` 了文件，媒体库留下失效索引；中文系统照片描述是“照片拍摄于…”；Android 16 选择器选中后要点“完成” | 推图后等媒体库里真有这张图再继续，结束时经媒体库删除；描述按中英文匹配；有“完成 / Done”就点 |

每次失败后都从头重跑整个脚本，最终一轮的结果为上表。改过的 `image_flow.py`、`voice_flow.py` 又在模拟器（Android 15 / API 35）上各跑一遍，均 8/8 通过，确认没有破坏原来的用法。截图、录屏、报告保存在本地台账目录；拍照一步拍到的真实画面不留存。
测试结束后：停止两个 `lan_forward.py` 和语音附加实例，手机恢复 `stayon false`、键盘切回原来的设置。
