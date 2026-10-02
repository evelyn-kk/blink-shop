-- 0004 回填已有文档的 product_id：文档的全部分块都指向同一个商品时，把该商品记到文档上。
-- 只更新 product_id 仍为空的行，可以重复执行。
UPDATE knowledge_documents d
JOIN (
  SELECT document_id, MIN(product_id) AS product_id
  FROM knowledge_chunks
  GROUP BY document_id
  HAVING COUNT(DISTINCT product_id) = 1 AND MIN(product_id) <> ''
) c ON c.document_id = d.document_id
SET d.product_id = c.product_id
WHERE d.product_id = '';
