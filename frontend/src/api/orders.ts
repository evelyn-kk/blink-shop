import type { Order, Page } from '../types/api';
import { request } from './http';

/** 商家只能看和发货本店订单；管理员看全部订单，可以代发货或取消待支付订单。 */
export type OrderScope = 'merchant' | 'admin';

export interface OrderQuery {
  status?: string;
  orderNo?: string;
  /** 仅管理员。 */
  merchantId?: string;
  page?: number;
  pageSize?: number;
}

export function listOrders(scope: OrderScope, q: OrderQuery): Promise<Page<Order>> {
  const search = new URLSearchParams();
  if (q.status) search.set('status', q.status);
  if (q.orderNo) search.set('order_no', q.orderNo);
  if (scope === 'admin' && q.merchantId) search.set('merchant_id', q.merchantId);
  if (q.page && q.page > 1) search.set('page', String(q.page));
  if (q.pageSize) search.set('page_size', String(q.pageSize));
  const text = search.toString();
  return request(`/${scope}/orders${text ? `?${text}` : ''}`);
}

export function getOrder(scope: OrderScope, id: string): Promise<Order> {
  return request(`/${scope}/orders/${encodeURIComponent(id)}`);
}

/** 发货：只有待发货（已支付）的订单可以发货。 */
export function shipOrder(scope: OrderScope, id: string): Promise<Order> {
  return request(`/${scope}/orders/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ status: 'shipped' }) });
}

/** 管理员取消待支付订单（库存回补、退券）。 */
export function cancelOrderAsAdmin(id: string, reason: string): Promise<Order> {
  return request(`/admin/orders/${encodeURIComponent(id)}`, {
    method: 'PATCH',
    body: JSON.stringify({ status: 'cancelled', reason }),
  });
}
