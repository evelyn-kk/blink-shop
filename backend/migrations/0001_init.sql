-- 0001 初始表结构。迁移文件发布后只追加、不修改（启动时校验 checksum）。
-- 使用 IF NOT EXISTS：MySQL DDL 不支持事务，迁移中途失败后可直接重跑。
-- 约定：主键为带前缀的字符串 ID；时间统一 UTC，DATETIME(3)；金额 DECIMAL(10,2)；JSON 列读取后由应用校验结构。

-- ---------- 身份 ----------

CREATE TABLE IF NOT EXISTS accounts (
  account_id    VARCHAR(64)  NOT NULL,
  username      VARCHAR(64)  NOT NULL,
  password_hash VARCHAR(255) NOT NULL COMMENT 'bcrypt',
  display_name  VARCHAR(128) NOT NULL,
  avatar_url    VARCHAR(512) NOT NULL DEFAULT '',
  phone         VARCHAR(32)  NOT NULL DEFAULT '',
  email         VARCHAR(128) NOT NULL DEFAULT '',
  role          VARCHAR(16)  NOT NULL COMMENT 'user/merchant/admin',
  merchant_id   VARCHAR(64)  NOT NULL DEFAULT '',
  status        VARCHAR(16)  NOT NULL DEFAULT 'active' COMMENT 'active/inactive/risk',
  deleted_at    DATETIME(3)  NULL COMMENT '软删：注销后保留记录，用户名不可复用',
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (account_id),
  UNIQUE KEY uk_accounts_username (username),
  KEY idx_accounts_role (role),
  KEY idx_accounts_merchant_id (merchant_id),
  KEY idx_accounts_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS auth_tokens (
  token_hash  CHAR(64)    NOT NULL COMMENT 'token 的 SHA-256 hex，不保存明文',
  account_id  VARCHAR(64) NOT NULL,
  created_at  DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  expires_at  DATETIME(3) NOT NULL,
  PRIMARY KEY (token_hash),
  KEY idx_auth_tokens_account_id (account_id),
  KEY idx_auth_tokens_expires_at (expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- ---------- 目录 ----------

CREATE TABLE IF NOT EXISTS merchants (
  merchant_id   VARCHAR(64)  NOT NULL,
  name          VARCHAR(128) NOT NULL,
  logo_url      VARCHAR(512) NOT NULL DEFAULT '',
  description   TEXT         NOT NULL,
  service_phone VARCHAR(64)  NOT NULL DEFAULT '',
  status        VARCHAR(16)  NOT NULL DEFAULT 'active',
  created_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at    DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (merchant_id),
  KEY idx_merchants_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS categories (
  category_id VARCHAR(64)  NOT NULL,
  parent_id   VARCHAR(64)  NOT NULL DEFAULT '' COMMENT '空为一级分类',
  name        VARCHAR(128) NOT NULL,
  sort_order  INT          NOT NULL DEFAULT 0,
  created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (category_id),
  KEY idx_categories_parent_id (parent_id, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS products (
  product_id            VARCHAR(64)   NOT NULL,
  merchant_id           VARCHAR(64)   NOT NULL,
  category_id           VARCHAR(64)   NOT NULL,
  name                  VARCHAR(128)  NOT NULL,
  brand                 VARCHAR(64)   NOT NULL DEFAULT '',
  image_url             VARCHAR(512)  NOT NULL DEFAULT '',
  image_urls_json       JSON          NOT NULL,
  price                 DECIMAL(10,2) NOT NULL,
  market_price          DECIMAL(10,2) NOT NULL DEFAULT 0.00,
  stock_quantity        INT           NOT NULL DEFAULT 0,
  stock_status          VARCHAR(16)   NOT NULL,
  tags_json             JSON          NOT NULL,
  selling_points_json   JSON          NOT NULL,
  recommend_reason      TEXT          NOT NULL,
  risk_notes_json       JSON          NOT NULL,
  attributes_json       JSON          NOT NULL,
  suitable_for_json     JSON          NOT NULL,
  not_suitable_for_json JSON          NOT NULL,
  description           TEXT          NOT NULL,
  status                VARCHAR(16)   NOT NULL DEFAULT 'active' COMMENT 'active/inactive/risk/deleted',
  sort_order            INT           NOT NULL DEFAULT 0,
  created_at            DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at            DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (product_id),
  KEY idx_products_merchant_id (merchant_id),
  KEY idx_products_category_id (category_id),
  KEY idx_products_status_sort (status, sort_order)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS product_skus (
  sku_id         VARCHAR(64)   NOT NULL,
  product_id     VARCHAR(64)   NOT NULL,
  sku_name       VARCHAR(128)  NOT NULL,
  price          DECIMAL(10,2) NOT NULL,
  stock_quantity INT           NOT NULL DEFAULT 0,
  stock_status   VARCHAR(16)   NOT NULL,
  specs_json     JSON          NOT NULL,
  is_default     BOOLEAN       NOT NULL DEFAULT FALSE,
  created_at     DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at     DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (sku_id),
  UNIQUE KEY uk_product_skus_product_sku (product_id, sku_id) COMMENT '库存粒度'
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- ---------- 文件与知识 ----------

CREATE TABLE IF NOT EXISTS stored_files (
  file_id          VARCHAR(64)   NOT NULL,
  account_id       VARCHAR(64)   NOT NULL,
  object_key       VARCHAR(512)  NOT NULL COMMENT '不可猜测，不对外返回',
  mime_type        VARCHAR(128)  NOT NULL,
  size_bytes       BIGINT        NOT NULL DEFAULT 0,
  content_hash     CHAR(64)      NOT NULL,
  storage_provider VARCHAR(32)   NOT NULL DEFAULT 'minio',
  source_url       VARCHAR(1024) NOT NULL DEFAULT '',
  created_at       DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (file_id),
  UNIQUE KEY uk_stored_files_object_key (object_key),
  KEY idx_stored_files_account_id (account_id),
  KEY idx_stored_files_content_hash (content_hash)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS knowledge_documents (
  document_id   VARCHAR(64)   NOT NULL,
  merchant_id   VARCHAR(64)   NOT NULL,
  title         VARCHAR(256)  NOT NULL,
  doc_type      VARCHAR(64)   NOT NULL,
  content       MEDIUMTEXT    NOT NULL,
  status        VARCHAR(16)   NOT NULL COMMENT 'uploaded/parsing/indexing/indexed/failed',
  chunk_count   INT           NOT NULL DEFAULT 0,
  source_url    VARCHAR(1024) NOT NULL DEFAULT '',
  content_hash  CHAR(64)      NOT NULL,
  metadata_json JSON          NULL,
  error_reason  VARCHAR(512)  NOT NULL DEFAULT '',
  created_at    DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at    DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (document_id),
  UNIQUE KEY uk_knowledge_documents_merchant_hash (merchant_id, content_hash) COMMENT '同一商家相同内容去重',
  KEY idx_knowledge_documents_status (status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS knowledge_chunks (
  chunk_id    VARCHAR(64)  NOT NULL,
  document_id VARCHAR(64)  NOT NULL,
  merchant_id VARCHAR(64)  NOT NULL,
  product_id  VARCHAR(64)  NOT NULL DEFAULT '',
  chunk_index INT          NOT NULL,
  title       VARCHAR(256) NOT NULL,
  content     TEXT         NOT NULL,
  source      VARCHAR(256) NOT NULL DEFAULT '',
  created_at  DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (chunk_id),
  UNIQUE KEY uk_knowledge_chunks_document_index (document_id, chunk_index),
  KEY idx_knowledge_chunks_merchant_id (merchant_id),
  KEY idx_knowledge_chunks_product_id (product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- ---------- 交易 ----------

CREATE TABLE IF NOT EXISTS cart_items (
  cart_item_id VARCHAR(64) NOT NULL,
  account_id   VARCHAR(64) NOT NULL,
  product_id   VARCHAR(64) NOT NULL,
  sku_id       VARCHAR(64) NOT NULL,
  quantity     INT         NOT NULL,
  selected     BOOLEAN     NOT NULL DEFAULT TRUE,
  created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (cart_item_id),
  UNIQUE KEY uk_cart_items_account_product_sku (account_id, product_id, sku_id),
  KEY idx_cart_items_product_id (product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS checkout_requests (
  request_id      VARCHAR(64)  NOT NULL,
  account_id      VARCHAR(64)  NOT NULL,
  idempotency_key VARCHAR(128) NOT NULL,
  status          VARCHAR(16)  NOT NULL COMMENT 'processing/succeeded/failed',
  order_ids_json  JSON         NOT NULL,
  created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (request_id),
  UNIQUE KEY uk_checkout_requests_account_key (account_id, idempotency_key)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS orders (
  order_id            VARCHAR(64)   NOT NULL,
  order_no            VARCHAR(64)   NOT NULL,
  account_id          VARCHAR(64)   NOT NULL,
  merchant_id         VARCHAR(64)   NOT NULL,
  checkout_request_id VARCHAR(64)   NOT NULL DEFAULT '',
  status              VARCHAR(32)   NOT NULL COMMENT 'pending_payment/paid/shipped/completed/cancelled',
  total_amount        DECIMAL(10,2) NOT NULL,
  discount_amount     DECIMAL(10,2) NOT NULL DEFAULT 0.00,
  pay_amount          DECIMAL(10,2) NOT NULL,
  payment_deadline_at DATETIME(3)   NULL,
  paid_at             DATETIME(3)   NULL,
  shipped_at          DATETIME(3)   NULL,
  completed_at        DATETIME(3)   NULL,
  closed_at           DATETIME(3)   NULL,
  cancel_reason       VARCHAR(256)  NOT NULL DEFAULT '',
  created_at          DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at          DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (order_id),
  UNIQUE KEY uk_orders_order_no (order_no),
  KEY idx_orders_account_created (account_id, created_at),
  KEY idx_orders_merchant_created (merchant_id, created_at),
  KEY idx_orders_status (status),
  KEY idx_orders_checkout_request_id (checkout_request_id),
  KEY idx_orders_payment_deadline_at (payment_deadline_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS order_items (
  order_item_id VARCHAR(64)   NOT NULL,
  order_id      VARCHAR(64)   NOT NULL,
  product_id    VARCHAR(64)   NOT NULL,
  sku_id        VARCHAR(64)   NOT NULL,
  name          VARCHAR(128)  NOT NULL COMMENT '下单时冻结',
  sku_name      VARCHAR(128)  NOT NULL DEFAULT '',
  image_url     VARCHAR(512)  NOT NULL DEFAULT '',
  price         DECIMAL(10,2) NOT NULL COMMENT '下单时冻结的单价',
  quantity      INT           NOT NULL,
  merchant_id   VARCHAR(64)   NOT NULL,
  merchant_name VARCHAR(128)  NOT NULL,
  created_at    DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (order_item_id),
  KEY idx_order_items_order_id (order_id),
  KEY idx_order_items_product_id (product_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS payments (
  payment_id     VARCHAR(64)   NOT NULL,
  order_id       VARCHAR(64)   NOT NULL,
  account_id     VARCHAR(64)   NOT NULL,
  amount         DECIMAL(10,2) NOT NULL,
  status         VARCHAR(16)   NOT NULL COMMENT 'pending/paid/closed',
  method         VARCHAR(32)   NOT NULL,
  transaction_no VARCHAR(96)   NOT NULL DEFAULT '',
  expires_at     DATETIME(3)   NOT NULL,
  paid_at        DATETIME(3)   NULL,
  created_at     DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at     DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (payment_id),
  KEY idx_payments_order_id (order_id),
  KEY idx_payments_account_id (account_id),
  KEY idx_payments_status_expires (status, expires_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- ---------- 营销与评价 ----------

CREATE TABLE IF NOT EXISTS promotion_rules (
  promotion_id     VARCHAR(64)   NOT NULL,
  name             VARCHAR(128)  NOT NULL,
  scope            VARCHAR(16)   NOT NULL COMMENT 'platform/merchant/product/category',
  merchant_id      VARCHAR(64)   NOT NULL DEFAULT '',
  product_id       VARCHAR(64)   NOT NULL DEFAULT '',
  category_id      VARCHAR(64)   NOT NULL DEFAULT '',
  type             VARCHAR(32)   NOT NULL COMMENT 'full_reduction/discount',
  threshold_amount DECIMAL(10,2) NOT NULL DEFAULT 0.00,
  discount_amount  DECIMAL(10,2) NOT NULL DEFAULT 0.00,
  discount_rate    DECIMAL(5,4)  NOT NULL DEFAULT 0.0000,
  stackable        BOOLEAN       NOT NULL DEFAULT TRUE,
  start_at         DATETIME(3)   NOT NULL,
  end_at           DATETIME(3)   NOT NULL,
  status           VARCHAR(16)   NOT NULL DEFAULT 'active',
  created_at       DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at       DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (promotion_id),
  KEY idx_promotion_rules_merchant_id (merchant_id),
  KEY idx_promotion_rules_active (status, start_at, end_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS coupons (
  coupon_id        VARCHAR(64)   NOT NULL,
  name             VARCHAR(128)  NOT NULL,
  scope            VARCHAR(16)   NOT NULL COMMENT 'platform/merchant',
  merchant_id      VARCHAR(64)   NOT NULL DEFAULT '',
  type             VARCHAR(32)   NOT NULL COMMENT 'fixed_amount',
  threshold_amount DECIMAL(10,2) NOT NULL DEFAULT 0.00,
  discount_amount  DECIMAL(10,2) NOT NULL DEFAULT 0.00,
  total_count      INT           NOT NULL DEFAULT 0,
  claimed_count    INT           NOT NULL DEFAULT 0 COMMENT '领券时条件更新，保证不超发',
  per_user_limit   INT           NOT NULL DEFAULT 1,
  start_at         DATETIME(3)   NOT NULL,
  end_at           DATETIME(3)   NOT NULL,
  status           VARCHAR(16)   NOT NULL DEFAULT 'active',
  created_at       DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at       DATETIME(3)   NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (coupon_id),
  KEY idx_coupons_merchant_id (merchant_id),
  KEY idx_coupons_active (status, start_at, end_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS user_coupons (
  user_coupon_id VARCHAR(64) NOT NULL,
  coupon_id      VARCHAR(64) NOT NULL,
  account_id     VARCHAR(64) NOT NULL,
  status         VARCHAR(16) NOT NULL DEFAULT 'unused' COMMENT 'unused/used/expired',
  order_id       VARCHAR(64) NOT NULL DEFAULT '',
  claimed_at     DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  used_at        DATETIME(3) NULL,
  PRIMARY KEY (user_coupon_id),
  KEY idx_user_coupons_account_status (account_id, status),
  KEY idx_user_coupons_coupon_account (coupon_id, account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS product_reviews (
  review_id           VARCHAR(64) NOT NULL,
  order_id            VARCHAR(64) NOT NULL,
  order_item_id       VARCHAR(64) NOT NULL,
  product_id          VARCHAR(64) NOT NULL,
  sku_id              VARCHAR(64) NOT NULL DEFAULT '',
  account_id          VARCHAR(64) NOT NULL,
  rating              TINYINT     NOT NULL,
  content             TEXT        NOT NULL,
  tags_json           JSON        NOT NULL,
  status              VARCHAR(16) NOT NULL DEFAULT 'visible' COMMENT 'visible/hidden',
  merchant_reply      TEXT        NULL,
  merchant_replied_at DATETIME(3) NULL,
  created_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at          DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (review_id),
  UNIQUE KEY uk_product_reviews_order_item (order_item_id) COMMENT '一个订单项只能评价一次',
  KEY idx_product_reviews_product_status (product_id, status),
  KEY idx_product_reviews_account_id (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- ---------- 导购会话 ----------

CREATE TABLE IF NOT EXISTS chat_sessions (
  session_id      VARCHAR(64)  NOT NULL,
  account_id      VARCHAR(64)  NOT NULL,
  title           VARCHAR(128) NOT NULL,
  summary         TEXT         NULL,
  message_count   INT          NOT NULL DEFAULT 0,
  last_message_at DATETIME(3)  NULL,
  pinned_at       DATETIME(3)  NULL,
  deleted_at      DATETIME(3)  NULL COMMENT '软删',
  created_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at      DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (session_id),
  KEY idx_chat_sessions_account_updated (account_id, updated_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS user_messages (
  message_id        VARCHAR(64)  NOT NULL,
  session_id        VARCHAR(64)  NOT NULL,
  account_id        VARCHAR(64)  NOT NULL,
  client_message_id VARCHAR(128) NOT NULL,
  content           TEXT         NOT NULL,
  attachments_json  JSON         NOT NULL,
  created_at        DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (message_id),
  UNIQUE KEY uk_user_messages_client_message (account_id, session_id, client_message_id) COMMENT '流式请求幂等',
  KEY idx_user_messages_session_created (session_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_runs (
  run_id               VARCHAR(64) NOT NULL,
  session_id           VARCHAR(64) NOT NULL,
  message_id           VARCHAR(64) NOT NULL,
  account_id           VARCHAR(64) NOT NULL,
  trace_id             VARCHAR(64) NOT NULL,
  status               VARCHAR(16) NOT NULL COMMENT 'queued/running/completed/failed/cancelled',
  content              MEDIUMTEXT  NULL,
  blocks_json          JSON        NULL,
  followups_json       JSON        NULL,
  prompt_versions_json JSON        NULL COMMENT '本次运行生效的 Prompt 版本',
  error_code           VARCHAR(64) NOT NULL DEFAULT '',
  created_at           DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at           DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (run_id),
  UNIQUE KEY uk_agent_runs_account_message (account_id, message_id) COMMENT '一条消息只运行一次',
  KEY idx_agent_runs_session_id (session_id),
  KEY idx_agent_runs_trace_id (trace_id),
  KEY idx_agent_runs_created_at (created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_trace_events (
  trace_event_id VARCHAR(64)  NOT NULL,
  run_id         VARCHAR(64)  NOT NULL,
  trace_id       VARCHAR(64)  NOT NULL,
  account_id     VARCHAR(64)  NOT NULL,
  stage          VARCHAR(64)  NOT NULL,
  event_type     VARCHAR(64)  NOT NULL,
  model          VARCHAR(128) NOT NULL DEFAULT '',
  status         VARCHAR(16)  NOT NULL,
  duration_ms    BIGINT       NOT NULL DEFAULT 0,
  error          TEXT         NULL,
  metadata_json  JSON         NULL,
  created_at     DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (trace_event_id),
  KEY idx_agent_trace_events_run_created (run_id, created_at),
  KEY idx_agent_trace_events_trace_id (trace_id),
  KEY idx_agent_trace_events_account_id (account_id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

-- ---------- 治理 ----------

CREATE TABLE IF NOT EXISTS agent_prompts (
  prompt_id    VARCHAR(64)  NOT NULL,
  prompt_key   VARCHAR(128) NOT NULL,
  title        VARCHAR(128) NOT NULL,
  content      MEDIUMTEXT   NOT NULL,
  status       VARCHAR(16)  NOT NULL COMMENT 'draft/active/archived',
  version      INT          NOT NULL,
  description  TEXT         NULL,
  created_by   VARCHAR(64)  NOT NULL DEFAULT '',
  published_at DATETIME(3)  NULL,
  created_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  updated_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3) ON UPDATE CURRENT_TIMESTAMP(3),
  PRIMARY KEY (prompt_id),
  UNIQUE KEY uk_agent_prompts_key_version (prompt_key, version),
  KEY idx_agent_prompts_key_status (prompt_key, status)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS agent_prompt_publish_records (
  record_id    VARCHAR(64)  NOT NULL,
  prompt_key   VARCHAR(128) NOT NULL,
  prompt_id    VARCHAR(64)  NOT NULL,
  version      INT          NOT NULL,
  published_by VARCHAR(64)  NOT NULL DEFAULT '',
  target       VARCHAR(128) NOT NULL DEFAULT '' COMMENT '发布目标，如 Nacos data id',
  result       VARCHAR(16)  NOT NULL COMMENT 'success/fallback/failed',
  created_at   DATETIME(3)  NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
  PRIMARY KEY (record_id),
  KEY idx_agent_prompt_publish_key_created (prompt_key, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
