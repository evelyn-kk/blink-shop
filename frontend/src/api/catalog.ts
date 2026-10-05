import type {
  CategoryNode,
  Merchant,
  Page,
  ProductCard,
  ProductDetail,
  ProductSku,
  Promotion,
  PublicReview,
} from '../types/api';
import { request } from './http';

export interface ProductQuery {
  keyword?: string;
  categoryId?: string;
  page?: number;
  pageSize?: number;
}

function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '') {
      search.set(key, String(value));
    }
  }
  const text = search.toString();
  return text ? `?${text}` : '';
}

export async function getCategoryTree(): Promise<CategoryNode[]> {
  const res = await request<{ items: CategoryNode[] }>('/categories/tree');
  return res.items;
}

export function listProducts(q: ProductQuery): Promise<Page<ProductCard>> {
  return request(
    `/products${query({ keyword: q.keyword, category_id: q.categoryId, page: q.page, page_size: q.pageSize })}`,
  );
}

export function getProduct(id: string): Promise<ProductDetail> {
  return request(`/products/${encodeURIComponent(id)}`);
}

export function listSkus(id: string): Promise<Page<ProductSku>> {
  return request(`/products/${encodeURIComponent(id)}/skus${query({ page_size: 100 })}`);
}

export function listReviews(id: string, page: number): Promise<Page<PublicReview>> {
  return request(`/products/${encodeURIComponent(id)}/reviews${query({ page, page_size: 10 })}`);
}

export function listPromotionsFor(productId: string): Promise<Page<Promotion>> {
  return request(`/promotions${query({ product_id: productId, page_size: 100 })}`);
}

/** 营业中的商家（公开接口），管理端用于选择采集的归属商家。 */
export function listMerchants(): Promise<Page<Merchant>> {
  return request('/merchants?page_size=100');
}
