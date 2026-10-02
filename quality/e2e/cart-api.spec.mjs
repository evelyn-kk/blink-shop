import { expect, test } from '@playwright/test';

// 购物车与优惠券：直接调用真实 API（真实 MySQL），覆盖并发加购、金额试算和领券后的用券。
// Android 购物车页在 6.2 接入；这里验证它要用到的接口。每个用例注册新用户，重复运行互不影响。

async function newUser(request, tag) {
  // 账号最长 32 个字符：前缀 + 项目首字母 + 时间戳后 9 位 + 随机数。
  const username = `cart_${tag}_${String(Date.now()).slice(-9)}_${Math.floor(Math.random() * 1e5)}`;
  const res = await request.post('/api/v1/auth/register', {
    data: { username, password: 'E2e-Cart#2026', display_name: '购物车测试' },
  });
  expect(res.status(), await res.text()).toBe(201);
  const { token } = await res.json();
  const call = async (method, path, data) => {
    const r = await request.fetch(`/api/v1${path}`, { method, data, headers: { Authorization: `Bearer ${token}` } });
    return { status: r.status(), body: await r.json() };
  };
  return { call };
}

test('加购 → 试算 → 领券后自动用券，金额与规则一致', async ({ request }, testInfo) => {
  const u = await newUser(request, testInfo.project.name[0]);
  expect((await u.call('GET', '/cart')).body.items).toEqual([]);

  for (const body of [{ product_id: 'p_seed_earbuds' }, { product_id: 'p_seed_nova', sku_id: 'sku_seed_nova_128' }, { product_id: 'p_seed_lamp' }]) {
    expect((await u.call('POST', '/cart/items', body)).status).toBe(200);
  }
  // 新用户没有券：耳机 95 折（不可叠加）29.95 + 数码店满 1000 减 80 + 平台满 300 减 30。
  let preview = (await u.call('GET', '/cart/discount-preview')).body;
  expect(preview).toMatchObject({ total_amount: '3847.00', discount_amount: '139.95', pay_amount: '3707.05', user_coupon_ids: [] });
  expect(preview.lines.map((l) => `${l.id}=${l.amount}`)).toEqual([
    'promo_seed_earbuds=29.95',
    'promo_seed_digital=80.00',
    'promo_seed_platform=30.00',
  ]);

  // 领店铺券和平台券后自动使用：店铺券 50（Nova 优惠后 2891.36 ≥ 500）+ 平台券 20。
  const shop = await u.call('POST', '/coupons/coupon_seed_digital:claim');
  expect(shop.status).toBe(200);
  expect((await u.call('POST', '/coupons/coupon_seed_platform:claim')).status).toBe(200);
  expect((await u.call('POST', '/coupons/coupon_seed_platform:claim')).body.code).toBe('coupon_limit_reached');
  preview = (await u.call('GET', '/cart/discount-preview')).body;
  expect(preview).toMatchObject({ discount_amount: '209.95', pay_amount: '3637.05' });
  expect(preview.user_coupon_ids).toHaveLength(2);
  expect(preview.user_coupon_ids).toContain(shop.body.user_coupon_id);

  // 指定只用店铺券；指定空值不用券。
  const onlyShop = (await u.call('GET', `/cart/discount-preview?user_coupon_ids=${shop.body.user_coupon_id}`)).body;
  expect(onlyShop.pay_amount).toBe('3657.05');
  expect((await u.call('GET', '/cart/discount-preview?user_coupon_ids=')).body.pay_amount).toBe('3707.05');

  // 购物车 summary 与默认试算一致。
  const cart = (await u.call('GET', '/cart')).body;
  expect(cart.summary).toMatchObject({ item_count: 3, selected_count: 3, total_amount: '3847.00', pay_amount: '3637.05' });
  const mine = (await u.call('GET', '/coupons/mine?status=unused')).body;
  expect(mine.total).toBe(2);
});

test('并发加购同一规格只有一行，数量不丢；越权和非法数量被拒', async ({ request }, testInfo) => {
  const u = await newUser(request, `${testInfo.project.name[0]}a`);
  const other = await newUser(request, `${testInfo.project.name[0]}b`);
  const results = await Promise.all(Array.from({ length: 10 }, () => u.call('POST', '/cart/items', { product_id: 'p_seed_mouse' })));
  expect(results.map((r) => r.status)).toEqual(Array(10).fill(200));
  const cart = (await u.call('GET', '/cart')).body;
  expect(cart.items).toHaveLength(1);
  expect(cart.items[0].quantity).toBe(10);

  const id = cart.items[0].cart_item_id;
  expect((await other.call('PATCH', `/cart/items/${id}`, { quantity: 1 })).status).toBe(404);
  expect((await other.call('DELETE', `/cart/items/${id}`)).status).toBe(404);
  expect((await u.call('PATCH', `/cart/items/${id}`, { quantity: 0 })).body).toMatchObject({ code: 'invalid_argument', field: 'quantity' });
  expect((await u.call('PATCH', `/cart/items/${id}`, { quantity: 151 })).status).toBe(400);
  expect((await u.call('POST', '/cart/items', { product_id: 'p_seed_earbuds', quantity: 6 })).body.code).toBe('insufficient_stock');
  expect((await u.call('POST', '/cart/items', { product_id: 'p_seed_keyboard' })).body.code).toBe('out_of_stock');
  expect((await u.call('GET', '/cart')).body.items).toHaveLength(1);
  expect((await u.call('DELETE', `/cart/items/${id}`)).body.items).toEqual([]);
});
