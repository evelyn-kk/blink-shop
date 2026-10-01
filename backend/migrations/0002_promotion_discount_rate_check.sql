-- 0002 促销折扣率限定在 [0, 1]。
-- DECIMAL(5,4) 本身允许 -9.9999 到 9.9999；应用层 domain.Rate 已在各入口校验，这里在数据库层兜底，
-- 防止绕过应用的写入（手工 SQL、脚本）产生超过 100% 或负数的折扣。
-- 单条 ALTER 语句，MySQL 8 中原子执行：要么加上约束，要么什么都不变，失败后可直接重跑。
ALTER TABLE promotion_rules
  ADD CONSTRAINT chk_promotion_rules_discount_rate CHECK (discount_rate >= 0 AND discount_rate <= 1);
