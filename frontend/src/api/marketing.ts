import type { MerchantPromotion, MerchantReview, Page, PromotionInput } from '../types/api';
import { request } from './http';

function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '' && !(key === 'page' && value === 1)) search.set(key, String(value));
  }
  const text = search.toString();
  return text ? `?${text}` : '';
}

export function listPromotions(q: { status?: string; page?: number; pageSize?: number }): Promise<Page<MerchantPromotion>> {
  return request(`/merchant/promotions${query({ status: q.status, page: q.page, page_size: q.pageSize })}`);
}

export function getPromotion(id: string): Promise<MerchantPromotion> {
  return request(`/merchant/promotions/${encodeURIComponent(id)}`);
}

export function createPromotion(input: PromotionInput): Promise<MerchantPromotion> {
  return request('/merchant/promotions', { method: 'POST', body: JSON.stringify(input) });
}

export function updatePromotion(id: string, input: PromotionInput): Promise<MerchantPromotion> {
  return request(`/merchant/promotions/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify(input) });
}

export function listMerchantReviews(q: { replied?: string; page?: number; pageSize?: number }): Promise<Page<MerchantReview>> {
  return request(`/merchant/reviews${query({ replied: q.replied, page: q.page, page_size: q.pageSize })}`);
}

export function replyReview(id: string, reply: string): Promise<MerchantReview> {
  return request(`/merchant/reviews/${encodeURIComponent(id)}:reply`, { method: 'POST', body: JSON.stringify({ reply }) });
}
