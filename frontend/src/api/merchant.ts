import type { MerchantProduct, Page, ProductInput } from '../types/api';
import { request } from './http';

export interface MerchantProductQuery {
  status?: string;
  keyword?: string;
  page?: number;
  pageSize?: number;
}

export function listMerchantProducts(q: MerchantProductQuery): Promise<Page<MerchantProduct>> {
  const search = new URLSearchParams();
  if (q.status) search.set('status', q.status);
  if (q.keyword) search.set('keyword', q.keyword);
  if (q.page && q.page > 1) search.set('page', String(q.page));
  if (q.pageSize) search.set('page_size', String(q.pageSize));
  const text = search.toString();
  return request(`/merchant/products${text ? `?${text}` : ''}`);
}

export function getMerchantProduct(id: string): Promise<MerchantProduct> {
  return request(`/merchant/products/${encodeURIComponent(id)}`);
}

export function createMerchantProduct(input: ProductInput): Promise<MerchantProduct> {
  return request('/merchant/products', { method: 'POST', body: JSON.stringify(input) });
}

export function updateMerchantProduct(id: string, input: ProductInput): Promise<MerchantProduct> {
  return request(`/merchant/products/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(input) });
}

export function deleteMerchantProduct(id: string): Promise<{ product_id: string; status: 'deleted' }> {
  return request(`/merchant/products/${encodeURIComponent(id)}`, { method: 'DELETE' });
}
