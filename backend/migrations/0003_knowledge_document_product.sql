-- 0003 知识文档记录关联的商品（可为空），商家资料可以挂在自己的某个商品下，检索结果据此关联商品。
-- 同时为文档列表（按商家、最近修改排序）加索引。单条 ALTER，失败后可直接重跑。
ALTER TABLE knowledge_documents
  ADD COLUMN product_id VARCHAR(64) NOT NULL DEFAULT '' AFTER merchant_id,
  ADD KEY idx_knowledge_documents_merchant_updated (merchant_id, updated_at);
