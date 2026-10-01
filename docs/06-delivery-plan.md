# 可执行开发计划

每个阶段必须独立可构建、通过新增测试后再进入下一阶段。Agent 不得跨阶段用 mock 掩盖未实现的授权、事务或 API 契约。

| 阶段 | 交付物 | 完成检查 |
| --- | --- | --- |
| 0. 脚手架 | 三端目录、compose、`.env.example`、CI、OpenAPI skeleton | 空工程 lint/build，所有依赖可启动 |
| 1. 领域与身份 | migration、domain、Store interface/MySQL、bcrypt/token、RBAC middleware、种子 | 注册登录/me、越权 403、迁移可重复执行 |
| 2. 目录与文件 | 分类/商家/商品/SKU/评价/促销、MinIO 上传 | 公开浏览、商家只能改自己商品、MIME/大小校验 |
| 3. 交易 | cart、营销试算、订单/支付/评价状态机 | 并发结算不超卖，失败完全回滚，幂等通过 |
| 4. Web | 登录、商家工作台、平台工作台及各治理页 | 基于 OpenAPI mock 和真实 API 均可运行 |
| 5. Android 基础 | 认证、商品、详情、购物车、结算、订单、账户 | Debug APK 安装并走通用户闭环 |
| 6. Agent 基础 | session/run/SSE、规则规划、工具、trace、取消 | 无 LLM 密钥完成导购/加购/订单查询 |
| 7. RAG/多模态 | 文档入库、Milvus、图搜、附件、STT/TTS、浮窗 | 失败降级、引用和图片搜索可验证 |
| 8. 治理质量 | Nacos 配置/Prompt 发布、eval runner、管理观测、安全硬化 | 第 07 章所有门禁通过 |

## 建议提交切分

每个提交只含一个纵向能力及测试，例如 `feat(auth): add token authentication`，避免“搭框架 + 改 schema + 写 UI + 接模型”混合提交。PR 描述必须包含：关联阶段、OpenAPI 变化、迁移影响、测试命令、回滚方案、截图/录屏（有 UI 时）。

## Agent 执行循环

1. 读取相关章节与现有接口/迁移，先写或更新 OpenAPI fixture。
2. 实现最小完整服务端路径，再写客户端；不在客户端重复业务规则。
3. 添加单元与 HTTP 集成测试，运行格式化、lint、build。
4. 对照验收场景手动走一次，记录结果；API 或状态机变更必须同步所有文档。
