# Agent、RAG 与多模态实施规范

## 一次导购运行

```text
鉴权/限流 → 风险词检查 → 附件归一化 → 会话记忆检索
→ 小模型/规则规划意图 → 按意图白名单选择工具与技能
→ ReAct（工具观察最多 N 轮）→ 受控最终生成并 SSE 输出
→ 追问生成 → 持久化 run、segments、trace
```

规划意图至少覆盖：`guide`、`product_search`、`product_compare`、`image_search`、`cart`、`checkout`、`order`、`coupon`、`review`、`navigation`、`non_guide`。导航意图不得调用交易工具。没有模型时规划和最终回答均应回退到规则，而不是伪造模型成功。

## 工具安全边界

允许工具：`search_products`、`search_image_products`、`search_knowledge`、`get_cart`、`add/update/delete_cart_item`、`preview_discount`、`checkout`、`list/get/pay/cancel/confirm_receipt order`、`list/claim coupon`、`list promotions/reviews`、`create review`。工具参数须 JSON schema 校验。加购必须使用“当前轮搜索结果或用户明确指定”的 product_id；按“第一个”操作时先向用户确认或基于上一条可见商品卡。支付、取消、收货、评价必须先解析到当前用户订单。实现（意图白名单、来源校验、风险策略、轨迹）见 `backend/README.md`“导购规划与工具”，工具清单见 `backend/fixtures/agent/tools.json`。

## 检索

文本：规范化文档 → 分块（保留标题/来源/顺序）→ 嵌入并写入 Milvus → keyword/vector 候选融合 → rerank → 仅引用实际 chunk。商品：关键词/分类/库存初筛，结构化正负约束（预算、品牌、用途、禁忌）过滤后重排。图搜：服务端下载/读取已授权图片，生成图向量，在商品图集合近邻查询，结合商品可售性过滤。所有向量结果需要可解释的 source id 和 score。

## Prompt 与配置治理

Prompt 由 `prompt_key + version + status` 管理；发布时创建新版本、原子设 active、写发布记录并推送 Nacos。动态配置 15 秒以内刷新，密钥字段列表只显示掩码。每次运行 trace 记录 planner、prompt version、每个工具输入（脱敏）、检索 chunk ids、模型名、耗时、错误和最终状态。

## 向导、风控、语音

浮窗建议按页面生成 1–3 条短问题：规则候选 → 上下文增强 → 小模型改写排序 → 长度/重复/相关性校验 → 规则兜底；页面支持 products/cart/orders/product_detail。风险检查位于模型调用前，命中阻断词要写审计并给出安全替代说明。实时 STT/TTS 仅经后端代理，密钥不进入 APK；文本朗读前清洗 markdown、长度限制并提供关闭入口。
