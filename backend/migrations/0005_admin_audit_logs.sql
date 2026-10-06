-- 0005 管理员操作审计：账号/商家/商品/促销/评价的状态变更和配置修改，与变更本身在同一个事务里写入，管理端可按对象查询。
-- 密钥类配置的前后值只记录掩码。
CREATE TABLE IF NOT EXISTS admin_audit_logs (
  audit_id      VARCHAR(64)  NOT NULL,
  seq           BIGINT       NOT NULL AUTO_INCREMENT COMMENT '写入顺序：同一毫秒的多条记录也能按先后排序',
  operator_id   VARCHAR(64)  NOT NULL,
  operator_name VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '操作时的显示名（账号注销后仍可读）',
  action        VARCHAR(64)  NOT NULL COMMENT '例如 account.status_changed、config.updated',
  target_type   VARCHAR(32)  NOT NULL COMMENT 'account/merchant/product/promotion/review/config',
  target_id     VARCHAR(128) NOT NULL,
  before_value  VARCHAR(512) NOT NULL DEFAULT '',
  after_value   VARCHAR(512) NOT NULL DEFAULT '',
  reason        VARCHAR(256) NOT NULL DEFAULT '',
  request_id    VARCHAR(64)  NOT NULL DEFAULT '',
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (audit_id),
  UNIQUE KEY uk_admin_audit_seq (seq),
  KEY idx_admin_audit_target (target_type, target_id, created_at),
  KEY idx_admin_audit_created (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
