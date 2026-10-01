# 数据模型、状态机与不变量

## 聚合与核心表

| 聚合 | 表 | 关键字段/约束 |
| --- | --- | --- |
| 目录 | `merchants`,`categories`,`products`,`product_skus` | 商品归属商家；状态 active/inactive/deleted/risk；`(product_id, sku_id)` 是库存粒度 |
| 身份 | `accounts`,`auth_tokens` | username 唯一；token 仅存 SHA-256 摘要；账户 active/inactive/risk，软删 `deleted_at` |
| 导购 | `chat_sessions`,`user_messages`,`agent_runs`,`agent_trace_events` | `(account_id, session_id, client_message_id)` 唯一；`(account_id,message_id)` 唯一，确保流式请求幂等 |
| 知识 | `knowledge_documents`,`knowledge_chunks` | 文档按 `(merchant_id, content_hash)` 去重，记录 source/metadata/分块数 |
| 交易 | `cart_items`,`orders`,`order_items`,`payments` | 购物车唯一键 `(account_id, product_id, sku_id)`；订单冻结商品名称/图片/价格 |
| 营销与评价 | `promotion_rules`,`coupons`,`user_coupons`,`product_reviews` | 评价 `order_item_id` 唯一；领券、使用均须并发安全 |
| 治理 | `agent_prompts`,`agent_prompt_publish_records` | `(prompt_key, version)` 唯一；密钥类 config 永不回传明文 |

金额使用 MySQL `DECIMAL(10,2)` 和后端定点/十进制类型；绝不以 float 计算金额。JSON 列保存标签、属性、附件、块，读取后必须校验结构。

## 状态机

```text
订单：pending_payment -> paid -> shipped -> completed
             |             |
             v             v
          cancelled      （禁止取消）
支付：pending -> paid | closed
商品/账户/商家：active <-> inactive；任意 -> risk；商品可 -> deleted
Agent run：queued -> running -> completed | failed | cancelled
文档：uploaded -> parsing -> indexing -> indexed | failed
```

只允许服务端状态迁移；商家仅能把自己 `paid` 订单置为 `shipped`，用户仅能支付/取消自己的待支付订单、确认自己的已发货订单，并仅能评价已完成且未评价订单项。管理员具有审核运营范围，但也不能绕过库存与金额约束。

## 交易事务（不可拆）

结算时按商家分组。一个数据库事务内：`SELECT ... FOR UPDATE` 锁定被选购物车项和 SKU/商品库存 → 验证 active/库存/价格 → 计算当前有效促销与优惠券 → 创建订单、订单项、支付单 → 扣减库存 → 清除已结算购物车项 → 提交。任一步失败必须整体回滚。重复结算请求需要 idempotency key；返回首次成功订单快照。

## 数据迁移与种子

迁移文件只追加、不修改已发布版本；本地可启动时自动迁移，生产仅显式 `RUN_MIGRATIONS=true` 的迁移 job 可执行。种子数据应包括管理员、商家、用户、两级分类、至少 6 个可售/不可售商品、SKU、知识、促销券和覆盖每个订单状态的样例；密码仅用 bcrypt hash。
