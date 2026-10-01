# 质量、安全与验收

## 自动化门禁

- 后端：`go test ./...`、`go vet ./...`、race 检查（CI nightly）；Store 用事务集成测试覆盖。
- Web：TypeScript strict、`npm run lint`、`npm run build`、API client contract test。
- Android：`./gradlew test lint assembleDebug`；关键流程至少有 instrumentation/UI smoke test。
- 接口：OpenAPI validation + 授权矩阵 + SSE fixture test；schema 变更触发兼容性检查。
- 质量：意图、RAG recall、商品检索、图搜、Agent E2E JSONL 数据集；报告含版本、模型配置指纹、通过率和失败样本。

## 安全基线

密码 bcrypt；token 随机生成并仅保存 hash，登出即撤销；CORS 生产白名单；限流在认证前后分层；请求体和文件大小限制；服务端 MIME 嗅探；路径与 SSRF 防护；结构化日志脱敏 Authorization、密码、密钥、个人信息；错误不回显 SQL/堆栈。所有 SQL 参数化，所有 merchant/admin 资源同时校验 role 与资源归属。生产拒绝 demo 凭据与自动建库。

## E2E 验收清单

1. 用户注册→登录→筛选商品→详情→加购→优惠试算→下单→支付模拟→商家发货→用户收货→评价。
2. 同一购物车并发两次 checkout：只生成一组订单/支付单，库存不为负；库存不足时无半成品订单。
3. 相同 `client_message_id` 重发：不产生第二次模型调用或第二条 user_message；取消 SSE 后 run 是 cancelled。
4. 无 AI/Milvus/MinIO 时：商品交易仍可用，Agent 说明降级且不虚构引用；图搜/上传返回规范错误。
5. 用户 A 不能读取用户 B 的文件、会话、购物车、订单；商家 A 不能修改商家 B 商品；普通用户不能访问 `/admin/*`。
6. 上传伪造 MIME、超限文件、恶意 URL、非法订单状态迁移、过期券、重复评价均被拒绝且错误码稳定。
7. 管理员发布 Prompt/config 后运行 trace 可关联生效版本；列表中 secret 被掩码。
8. Android Debug 从冷启动完成导购图文流式、图片附件、购物车与订单；断网/拒权/服务未配置可恢复。

## 发布标准

所有门禁绿、迁移经空库与升级库验证、生产配置校验通过、备份/回滚步骤演练、SLO 日志和错误告警可用。发现数据越权、密钥泄露、超卖或不可逆迁移时，停止发布并先修复。
