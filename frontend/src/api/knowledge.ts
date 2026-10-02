import type {
  DocumentInput,
  IngestionInput,
  IngestionResult,
  KnowledgeDocument,
  KnowledgeDocumentDetail,
  Page,
} from '../types/api';
import { request } from './http';

/** 商家只能访问自己的资料；管理员可以访问全部资料并为任意商家或平台采集。 */
export type DocumentScope = 'merchant' | 'admin';

export interface DocumentQuery {
  status?: string;
  keyword?: string;
  /** 仅管理员：undefined 为全部，'' 为平台资料。 */
  merchantId?: string;
  page?: number;
  pageSize?: number;
}

export function listDocuments(scope: DocumentScope, q: DocumentQuery): Promise<Page<KnowledgeDocument>> {
  const search = new URLSearchParams();
  if (q.status) search.set('status', q.status);
  if (q.keyword) search.set('keyword', q.keyword);
  if (scope === 'admin' && q.merchantId !== undefined) search.set('merchant_id', q.merchantId);
  if (q.page && q.page > 1) search.set('page', String(q.page));
  if (q.pageSize) search.set('page_size', String(q.pageSize));
  const text = search.toString();
  return request(`/${scope}/documents${text ? `?${text}` : ''}`);
}

export function getDocument(scope: DocumentScope, id: string): Promise<KnowledgeDocumentDetail> {
  return request(`/${scope}/documents/${encodeURIComponent(id)}`);
}

/** 商家提交文字资料，返回文档本身（新建 201，重复内容 200 返回已有文档）。 */
export function createMerchantDocument(input: DocumentInput): Promise<KnowledgeDocument> {
  return request('/merchant/documents', { method: 'POST', body: JSON.stringify(input) });
}

export function ingestDocument(scope: DocumentScope, input: IngestionInput): Promise<IngestionResult> {
  return request(`/${scope}/unstructured-ingestions`, { method: 'POST', body: JSON.stringify(input) });
}
