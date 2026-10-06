import { useSyncExternalStore } from 'react';
import type { DocumentScope } from '../api/knowledge';
import type { OrderScope } from '../api/orders';
import type { Role } from '../types/api';

// 轻量 hash 路由：#/products?keyword=..、#/products/:id。5.1 接入登录与角色路由时再替换为正式路由。
export type Route =
  | { name: 'products'; query: URLSearchParams }
  | { name: 'product'; id: string }
  | { name: 'login'; next: string }
  | { name: 'merchant_products'; query: URLSearchParams }
  | { name: 'merchant_product_new' }
  | { name: 'merchant_product_edit'; id: string }
  | { name: 'documents'; scope: DocumentScope; query: URLSearchParams }
  | { name: 'document_new'; scope: DocumentScope }
  | { name: 'document'; scope: DocumentScope; id: string }
  | { name: 'merchant_promotions'; query: URLSearchParams }
  | { name: 'merchant_promotion_new' }
  | { name: 'merchant_promotion_edit'; id: string }
  | { name: 'merchant_reviews'; query: URLSearchParams }
  | { name: 'admin_platform'; query: URLSearchParams }
  | { name: 'admin_risk' }
  | { name: 'admin_configs' }
  | { name: 'admin_audit'; query: URLSearchParams }
  | { name: 'orders'; scope: OrderScope; query: URLSearchParams }
  | { name: 'order'; scope: OrderScope; id: string }
  | { name: 'not_found' };

export function parseRoute(hash: string): Route {
  const raw = hash.replace(/^#/, '') || '/products';
  const [path, search = ''] = raw.split('?', 2);
  const parts = path.split('/').filter(Boolean);
  if (parts[0] === 'products' && parts.length === 1) {
    return { name: 'products', query: new URLSearchParams(search) };
  }
  if (parts[0] === 'products' && parts.length === 2) {
    return { name: 'product', id: decodeURIComponent(parts[1]) };
  }
  if (parts[0] === 'login' && parts.length === 1) {
    const next = new URLSearchParams(search).get('next') ?? '';
    // 只允许站内 hash 地址，避免被构造成跳转到外部。
    // 不允许回跳到登录页自身，避免循环。
    return { name: 'login', next: next.startsWith('#/') && !next.startsWith('#/login') ? next : '' };
  }
  if (parts[0] === 'merchant' && parts[1] === 'products') {
    if (parts.length === 2) return { name: 'merchant_products', query: new URLSearchParams(search) };
    if (parts.length === 3 && parts[2] === 'new') return { name: 'merchant_product_new' };
    if (parts.length === 4 && parts[3] === 'edit') {
      return { name: 'merchant_product_edit', id: decodeURIComponent(parts[2]) };
    }
  }
  if ((parts[0] === 'merchant' || parts[0] === 'admin') && parts[1] === 'documents') {
    const scope: DocumentScope = parts[0];
    if (parts.length === 2) return { name: 'documents', scope, query: new URLSearchParams(search) };
    if (parts.length === 3 && parts[2] === 'new') return { name: 'document_new', scope };
    if (parts.length === 3) return { name: 'document', scope, id: decodeURIComponent(parts[2]) };
  }
  if (parts[0] === 'merchant' && parts[1] === 'promotions') {
    if (parts.length === 2) return { name: 'merchant_promotions', query: new URLSearchParams(search) };
    if (parts.length === 3 && parts[2] === 'new') return { name: 'merchant_promotion_new' };
    if (parts.length === 4 && parts[3] === 'edit') return { name: 'merchant_promotion_edit', id: decodeURIComponent(parts[2]) };
  }
  if (parts[0] === 'merchant' && parts[1] === 'reviews' && parts.length === 2) {
    return { name: 'merchant_reviews', query: new URLSearchParams(search) };
  }
  if (parts[0] === 'admin' && parts.length === 2) {
    if (parts[1] === 'platform') return { name: 'admin_platform', query: new URLSearchParams(search) };
    if (parts[1] === 'risk') return { name: 'admin_risk' };
    if (parts[1] === 'configs') return { name: 'admin_configs' };
    if (parts[1] === 'audit') return { name: 'admin_audit', query: new URLSearchParams(search) };
  }
  if ((parts[0] === 'merchant' || parts[0] === 'admin') && parts[1] === 'orders') {
    const scope: OrderScope = parts[0];
    if (parts.length === 2) return { name: 'orders', scope, query: new URLSearchParams(search) };
    if (parts.length === 3) return { name: 'order', scope, id: decodeURIComponent(parts[2]) };
  }
  return { name: 'not_found' };
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener('hashchange', onChange);
  return () => window.removeEventListener('hashchange', onChange);
}

export function useRoute(): Route {
  const hash = useSyncExternalStore(subscribe, () => window.location.hash);
  return parseRoute(hash);
}

export function productsHref(params: { keyword?: string; categoryId?: string; page?: number }): string {
  const q = new URLSearchParams();
  if (params.keyword) q.set('keyword', params.keyword);
  if (params.categoryId) q.set('category_id', params.categoryId);
  if (params.page && params.page > 1) q.set('page', String(params.page));
  const text = q.toString();
  return `#/products${text ? `?${text}` : ''}`;
}

export function productHref(id: string): string {
  return `#/products/${encodeURIComponent(id)}`;
}

// 最近一次访问的商品列表地址（含搜索条件和页码），详情页的“返回列表”回到这里。
let lastProductsHash = '#/products';

export function rememberProductsHash(hash: string): void {
  lastProductsHash = hash || '#/products';
}

export function lastProductsHref(): string {
  return lastProductsHash;
}

export function loginHref(next: string): string {
  return `#/login?next=${encodeURIComponent(next)}`;
}

export function merchantProductsHref(params: { status?: string; keyword?: string; page?: number } = {}): string {
  const q = new URLSearchParams();
  if (params.status) q.set('status', params.status);
  if (params.keyword) q.set('keyword', params.keyword);
  if (params.page && params.page > 1) q.set('page', String(params.page));
  const text = q.toString();
  return `#/merchant/products${text ? `?${text}` : ''}`;
}

export function merchantProductEditHref(id: string): string {
  return `#/merchant/products/${encodeURIComponent(id)}/edit`;
}

export function navigate(hash: string): void {
  window.location.hash = hash;
}

export function documentsHref(
  scope: DocumentScope,
  params: { status?: string; keyword?: string; merchantId?: string; page?: number } = {},
): string {
  const q = new URLSearchParams();
  if (params.status) q.set('status', params.status);
  if (params.keyword) q.set('keyword', params.keyword);
  if (scope === 'admin' && params.merchantId !== undefined) q.set('merchant_id', params.merchantId);
  if (params.page && params.page > 1) q.set('page', String(params.page));
  const text = q.toString();
  return `#/${scope}/documents${text ? `?${text}` : ''}`;
}

export function documentHref(scope: DocumentScope, id: string): string {
  return `#/${scope}/documents/${encodeURIComponent(id)}`;
}

export function newDocumentHref(scope: DocumentScope): string {
  return `#/${scope}/documents/new`;
}


export function ordersHref(
  scope: OrderScope,
  params: { status?: string; orderNo?: string; merchantId?: string; page?: number } = {},
): string {
  const q = new URLSearchParams();
  if (params.status) q.set('status', params.status);
  if (params.orderNo) q.set('order_no', params.orderNo);
  if (scope === 'admin' && params.merchantId) q.set('merchant_id', params.merchantId);
  if (params.page && params.page > 1) q.set('page', String(params.page));
  const text = q.toString();
  return `#/${scope}/orders${text ? `?${text}` : ''}`;
}

export function orderHref(scope: OrderScope, id: string): string {
  return `#/${scope}/orders/${encodeURIComponent(id)}`;
}

/** 登录后按角色进入的默认页面：商家进“我的商品”，管理员进“知识资料（全部）”，普通用户回到商品巡检。 */
export function homeHref(role: Role): string {
  if (role === 'merchant') return merchantProductsHref();
  if (role === 'admin') return documentsHref('admin');
  return productsHref({});
}

export function promotionsHref(params: { status?: string; page?: number } = {}): string {
  const q = new URLSearchParams();
  if (params.status) q.set('status', params.status);
  if (params.page && params.page > 1) q.set('page', String(params.page));
  const text = q.toString();
  return `#/merchant/promotions${text ? `?${text}` : ''}`;
}

export function promotionEditHref(id: string): string {
  return `#/merchant/promotions/${encodeURIComponent(id)}/edit`;
}

export function reviewsHref(params: { replied?: string; page?: number } = {}): string {
  const q = new URLSearchParams();
  if (params.replied) q.set('replied', params.replied);
  if (params.page && params.page > 1) q.set('page', String(params.page));
  const text = q.toString();
  return `#/merchant/reviews${text ? `?${text}` : ''}`;
}

function withQuery(base: string, params: Record<string, string | number | undefined>): string {
  const q = new URLSearchParams();
  for (const [k, v] of Object.entries(params)) {
    if (v !== undefined && v !== '' && !(k === 'page' && Number(v) <= 1)) q.set(k, String(v));
  }
  const text = q.toString();
  return `${base}${text ? `?${text}` : ''}`;
}

export function platformHref(params: { tab?: string; status?: string; keyword?: string; page?: number } = {}): string {
  return withQuery('#/admin/platform', params);
}

export function auditHref(params: { target_type?: string; target_id?: string; page?: number } = {}): string {
  return withQuery('#/admin/audit', params);
}
