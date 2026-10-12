-- 0009 公开商品搜索走全文索引：search_text = 名称 + 品牌 + 标签 + 卖点（JSON 转小写文本），ngram 分词（默认 2 字）。
-- 建索引时关掉 InnoDB 停用词：ngram 分出的二字词只要含停用词（a、in、on、at……）就不进索引，"air"、"home" 会搜不到。
-- 这个设置只在建索引（或重建表）时起作用，查询不受影响；以后重建 products 表也要先在同一会话里关掉。
SET SESSION innodb_ft_enable_stopword = OFF;
ALTER TABLE products
  ADD COLUMN search_text TEXT GENERATED ALWAYS AS (CONCAT_WS(' ', name, brand, LOWER(CAST(tags_json AS CHAR)), LOWER(CAST(selling_points_json AS CHAR)))) STORED,
  ADD FULLTEXT INDEX ft_products_search (search_text) WITH PARSER ngram;
SET SESSION innodb_ft_enable_stopword = DEFAULT;
