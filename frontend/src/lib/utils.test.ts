import { describe, expect, it } from 'vitest';
import { describePromotion, formatDateTime, formatMoney, orderStatusLabel, orderStatusTone } from './format';
import { parsePage } from './paging';
import { homeHref, loginHref, orderHref, ordersHref, parseRoute } from './router';

describe('格式化', () => {
  it('金额按人民币本地化，非法值原样显示', () => {
    expect(formatMoney('2999.00')).toBe('¥2,999.00');
    expect(formatMoney('0.50')).toBe('¥0.50');
    expect(formatMoney('abc')).toBe('abc');
  });
  it('时间按本地时区显示到分钟', () => {
    expect(formatDateTime('2026-10-01T08:00:00Z')).toMatch(/2026\/10\/01 \d{2}:00/);
    expect(formatDateTime('not a time')).toBe('not a time');
  });
  it('促销说明', () => {
    expect(describePromotion({ type: 'discount', discount_rate: '0.9500' } as never)).toBe('9.5 折');
    expect(describePromotion({ type: 'full_reduction', threshold_amount: '300.00', discount_amount: '30.00' } as never)).toBe('满 300 减 30');
  });
  it('每个订单状态都有文字和颜色', () => {
    for (const s of Object.keys(orderStatusLabel) as (keyof typeof orderStatusLabel)[]) {
      expect(orderStatusLabel[s]).toBeTruthy();
      expect(orderStatusTone[s]).toBeTruthy();
    }
  });
});

describe('页码', () => {
  it.each([
    [null, 1],
    ['', 1],
    ['3', 3],
    [' 7 ', 7],
    ['0', 1],
    ['-2', 1],
    ['1.9', 1],
    ['1e5', 1],
    ['0x10', 1],
    ['100001', 100000],
    ['99999999999999999999', 1],
  ])('%s → %d', (raw, want) => expect(parsePage(raw)).toBe(want));
});

describe('路由', () => {
  it('解析各页面地址', () => {
    expect(parseRoute('')).toMatchObject({ name: 'products' });
    expect(parseRoute('#/products/p_1')).toEqual({ name: 'product', id: 'p_1' });
    expect(parseRoute('#/merchant/orders?status=paid')).toMatchObject({ name: 'orders', scope: 'merchant' });
    expect(parseRoute('#/admin/orders/o_1')).toEqual({ name: 'order', scope: 'admin', id: 'o_1' });
    expect(parseRoute('#/user/orders')).toEqual({ name: 'not_found' });
    expect(parseRoute('#/nothing')).toEqual({ name: 'not_found' });
  });
  it('登录后只回到站内地址，不回到登录页自身', () => {
    expect(parseRoute(`#/login?next=${encodeURIComponent('#/merchant/orders')}`)).toEqual({ name: 'login', next: '#/merchant/orders' });
    expect(parseRoute(`#/login?next=${encodeURIComponent('https://evil.example')}`)).toEqual({ name: 'login', next: '' });
    expect(parseRoute(`#/login?next=${encodeURIComponent('#/login?next=x')}`)).toEqual({ name: 'login', next: '' });
    expect(loginHref('#/admin/orders')).toBe('#/login?next=%23%2Fadmin%2Forders');
  });
  it('按角色的默认页面', () => {
    expect(homeHref('merchant')).toBe('#/merchant/products');
    expect(homeHref('admin')).toBe('#/admin/documents');
    expect(homeHref('user')).toBe('#/products');
  });
  it('订单地址只给管理员带店铺筛选', () => {
    expect(ordersHref('merchant', { status: 'paid', merchantId: 'm_1', page: 2 })).toBe('#/merchant/orders?status=paid&page=2');
    expect(ordersHref('admin', { merchantId: 'm_1' })).toBe('#/admin/orders?merchant_id=m_1');
    expect(orderHref('admin', 'o/1')).toBe('#/admin/orders/o%2F1');
  });
});
