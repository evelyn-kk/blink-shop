// 与 backend/openapi.yaml 中 components.schemas 保持一致。

export interface ApiErrorBody {
  code: string;
  message: string;
  /** 校验失败的字段，如 "skus[1].price"。 */
  field?: string;
  request_id?: string;
}

export interface Page<T> {
  page: number;
  page_size: number;
  total: number;
  items: T[];
}

export interface HealthResponse {
  status: 'ok';
}

// ---------- 公开目录（openapi tag: catalog） ----------

/** 两位小数的金额字符串，如 "2999.00"。 */
export type Money = string;

export type StockStatus = 'in_stock' | 'low_stock' | 'out_of_stock';

export interface CategoryNode {
  category_id: string;
  parent_id: string;
  name: string;
  children: CategoryNode[];
}

export interface Merchant {
  merchant_id: string;
  name: string;
  logo_url: string;
  description: string;
  service_phone: string;
}

export interface ProductCard {
  product_id: string;
  sku_id: string;
  merchant_id: string;
  merchant_name: string;
  category_id: string;
  name: string;
  brand: string;
  image_url: string;
  price: Money;
  market_price: Money;
  stock_status: StockStatus;
  tags: string[];
  selling_points: string[];
  recommend_reason: string;
  risk_notes: string[];
}

export interface ProductAttribute {
  key: string;
  value: string;
  unit: string;
}

export interface ProductDetail extends ProductCard {
  image_urls: string[];
  stock_quantity: number;
  attributes: ProductAttribute[];
  suitable_for: string[];
  not_suitable_for: string[];
  description: string;
}

export interface ProductSku {
  sku_id: string;
  product_id: string;
  sku_name: string;
  price: Money;
  stock_quantity: number;
  stock_status: StockStatus;
  specs: Record<string, string>;
  is_default: boolean;
}

export interface PublicReview {
  review_id: string;
  product_id: string;
  sku_id: string;
  reviewer_name: string;
  rating: number;
  content: string;
  tags: string[];
  merchant_reply: string;
  merchant_replied_at: string | null;
  created_at: string;
}

export interface Promotion {
  promotion_id: string;
  name: string;
  scope: 'platform' | 'merchant' | 'product' | 'category';
  merchant_id: string;
  product_id: string;
  category_id: string;
  type: 'full_reduction' | 'discount';
  threshold_amount: Money;
  discount_amount: Money;
  discount_rate: string;
  stackable: boolean;
  start_at: string;
  end_at: string;
}

// ---------- 认证 ----------

export type Role = 'user' | 'merchant' | 'admin';

export interface Account {
  account_id: string;
  username: string;
  display_name: string;
  avatar_url: string;
  phone: string;
  email: string;
  role: Role;
  merchant_id?: string;
  status: 'active' | 'inactive' | 'risk';
  created_at: string;
  updated_at: string;
}

export interface Session {
  token: string;
  token_type: 'Bearer';
  expires_at: string;
  account: Account;
}

// ---------- 商家商品 ----------

export type ProductStatus = 'active' | 'inactive' | 'risk';

export interface MerchantProduct {
  product_id: string;
  merchant_id: string;
  category_id: string;
  name: string;
  brand: string;
  image_url: string;
  image_urls: string[];
  price: Money;
  market_price: Money;
  stock_quantity: number;
  stock_status: StockStatus;
  tags: string[];
  selling_points: string[];
  recommend_reason: string;
  risk_notes: string[];
  attributes: ProductAttribute[];
  suitable_for: string[];
  not_suitable_for: string[];
  description: string;
  status: ProductStatus;
  skus: ProductSku[];
  created_at: string;
  updated_at: string;
}

export interface SkuInput {
  sku_id?: string;
  sku_name: string;
  price: string;
  stock_quantity: number;
  specs: Record<string, string>;
  is_default: boolean;
}

export interface ProductInput {
  name?: string;
  brand?: string;
  category_id?: string;
  image_url?: string;
  image_urls?: string[];
  market_price?: string;
  tags?: string[];
  selling_points?: string[];
  risk_notes?: string[];
  suitable_for?: string[];
  not_suitable_for?: string[];
  attributes?: ProductAttribute[];
  recommend_reason?: string;
  description?: string;
  status?: 'active' | 'inactive';
  skus?: SkuInput[];
}

// ---------- 私有文件（openapi tag: files） ----------

export type FileMimeType = 'image/jpeg' | 'image/png' | 'image/webp' | 'image/gif' | 'image/bmp' | 'application/pdf';

export interface StoredFile {
  file_id: string;
  /** 下载地址 /api/v1/files/{id}，需要登录，只有上传者本人和管理员可以读取。 */
  url: string;
  mime_type: FileMimeType;
  size_bytes: number;
  /** 内容 SHA-256（小写 hex）。 */
  content_hash: string;
  created_at: string;
}

export interface FileUpload {
  file: StoredFile;
}

// ---------- 知识文档（openapi tag: knowledge） ----------

export type DocumentStatus = 'uploaded' | 'parsing' | 'indexing' | 'indexed' | 'failed';

export type DocType =
  | 'product_detail'
  | 'faq'
  | 'policy'
  | 'guide'
  | 'web_article'
  | 'structured_note'
  | 'unstructured_note';

export interface KnowledgeDocument {
  document_id: string;
  /** 空串表示平台资料。 */
  merchant_id: string;
  product_id: string;
  title: string;
  doc_type: DocType;
  status: DocumentStatus;
  chunk_count: number;
  source_url: string;
  content_hash: string;
  metadata: Record<string, string | number | boolean | null>;
  error_reason: string;
  created_at: string;
  updated_at: string;
}

export interface KnowledgeChunk {
  chunk_id: string;
  chunk_index: number;
  title: string;
  content: string;
}

export interface KnowledgeDocumentDetail extends KnowledgeDocument {
  content: string;
  chunks: KnowledgeChunk[];
}

export interface DocumentInput {
  title: string;
  content: string;
  doc_type?: DocType;
  product_id?: string;
  force_reindex?: boolean;
}

/** content、html、json_text、source_url 必须且只能提供一个。 */
export interface IngestionInput {
  merchant_id?: string;
  title?: string;
  content?: string;
  html?: string;
  json_text?: string;
  source_url?: string;
  product_id?: string;
  force_reindex?: boolean;
}

export interface IngestionResult {
  document: KnowledgeDocument;
  duplicate: boolean;
  text_runes: number;
  truncated: boolean;
}

