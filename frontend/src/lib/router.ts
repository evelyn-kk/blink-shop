import { useSyncExternalStore } from 'react';

// 轻量 hash 路由：#/products?keyword=..、#/products/:id。5.1 接入登录与角色路由时再替换为正式路由。
export type Route =
  | { name: 'products'; query: URLSearchParams }
  | { name: 'product'; id: string }
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
