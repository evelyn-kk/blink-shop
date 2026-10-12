-- 0010 公开商品列表的计数（在售 + 商家正常）走覆盖索引：原来按 (status, sort_order) 找到每行再回表看 merchant_id，
-- 10 万商品时一次 COUNT 约 0.4s；有 (status, merchant_id) 后只扫索引。
ALTER TABLE products ADD KEY idx_products_status_merchant (status, merchant_id);
