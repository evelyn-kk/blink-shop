import { expect, test } from '@playwright/test';

// 订单闭环：买家一侧走真实 API（Android 订单页在 6.2 接入，这里验证它要用的接口），商家和管理员在 Web 上操作。
// 每个用例注册新买家并只买台灯（库存 60），不影响其他用例依赖的库存。

const PASSWORD = 'BlinkDev#2026';
const shots = (testInfo, name) => `reports/e2e/screens/${testInfo.project.name}-orders-${name}.png`;

async function apiLogin(request, username) {
  const res = await request.post('/api/v1/auth/login', { data: { username, password: PASSWORD } });
  expect(res.ok(), `login ${username}`).toBeTruthy();
  return res.json();
}

async function signedIn(page, session) {
  await page.addInitScript((s) => window.localStorage.setItem('blink_shop.session', JSON.stringify(s)), session);
}

async function newBuyer(request, tag) {
  const username = `ord_${tag}_${String(Date.now()).slice(-9)}_${Math.floor(Math.random() * 1e5)}`;
  const res = await request.post('/api/v1/auth/register', {
    data: { username, password: 'E2e-Order#2026', display_name: '订单测试' },
  });
  expect(res.status(), await res.text()).toBe(201);
  const { token } = await res.json();
  const call = async (method, path, data, headers = {}) => {
    const r = await request.fetch(`/api/v1${path}`, { method, data, headers: { Authorization: `Bearer ${token}`, ...headers } });
    return { status: r.status(), body: await r.json() };
  };
  return { call };
}

async function lampStock(request) {
  const res = await request.get('/api/v1/products/p_seed_lamp/skus');
  const { items } = await res.json();
  return items.find((s) => s.sku_id === 'sku_seed_lamp_white').stock_quantity;
}

test('下单 → 支付 → 商家在 Web 发货 → 买家确认收货 → 评价，商家详情看到已评价', async ({ page, request }, testInfo) => {
  const buyer = await newBuyer(request, testInfo.project.name[0]);
  const before = await lampStock(request);
  expect((await buyer.call('POST', '/cart/items', { product_id: 'p_seed_lamp', quantity: 2 })).status).toBe(200);
  const preview = (await buyer.call('GET', '/cart/discount-preview')).body;

  // 幂等：同一个键提交两次只下一单。
  const key = `e2e-${testInfo.project.name}-${Date.now()}`;
  const first = await buyer.call('POST', '/orders:checkout', { expected_pay_amount: preview.pay_amount }, { 'Idempotency-Key': key });
  expect(first.status, JSON.stringify(first.body)).toBe(201);
  const again = await buyer.call('POST', '/orders:checkout', {}, { 'Idempotency-Key': key });
  expect(again.status).toBe(200);
  expect(again.body.replayed).toBe(true);
  expect(again.body.items.map((o) => o.order_id)).toEqual(first.body.items.map((o) => o.order_id));
  const order = first.body.items[0];
  expect(order).toMatchObject({ status: 'pending_payment', pay_amount: preview.pay_amount, merchant_id: 'm_seed_home' });
  expect(await lampStock(request)).toBe(before - 2);
  expect((await buyer.call('GET', '/cart')).body.items).toEqual([]);

  const paid = await buyer.call('POST', `/orders/${order.order_id}:pay`, { method: 'mock_wechat' });
  expect(paid.status).toBe(200);
  expect(paid.body.payment).toMatchObject({ status: 'paid', method: 'mock_wechat' });

  // 家居店商家在 Web 上找到待发货订单并发货。
  await signedIn(page, await apiLogin(request, 'blink_merchant2'));
  await page.goto('/#/merchant/orders');
  await expect(page.getByRole('heading', { name: '订单', exact: true })).toBeVisible();
  await page.getByRole('link', { name: '待发货' }).click();
  await page.getByLabel('订单号').fill(order.order_no);
  await page.getByRole('button', { name: '查找' }).click();
  await expect(page.getByText('共 1 个订单')).toBeVisible();
  const row = page.getByRole('listitem').filter({ hasText: order.order_no });
  await expect(row.locator('.status')).toHaveText('待发货');
  await expect(row).toContainText('Blink 护眼台灯 L1 ×2');
  await page.screenshot({ path: shots(testInfo, 'merchant-list'), fullPage: true });

  // 发货需要确认；取消对话框不发货，确认后状态更新且按钮消失。
  await row.getByRole('button', { name: `发货 ${order.order_no}` }).click();
  await expect(page.getByRole('dialog')).toContainText('确认发货');
  await page.getByRole('button', { name: '取消', exact: true }).click();
  expect((await buyer.call('GET', `/orders/${order.order_id}`)).body.status).toBe('paid');
  await row.getByRole('button', { name: `发货 ${order.order_no}` }).click();
  await page.getByRole('dialog').getByRole('button', { name: '确认发货' }).click();
  await expect(page.locator('.flash')).toContainText(`订单 ${order.order_no} 已发货`);
  await expect(page.getByText('没有符合条件的订单')).toBeVisible(); // 待发货筛选下已经没有它
  expect((await buyer.call('GET', `/orders/${order.order_id}`)).body.status).toBe('shipped');

  // 买家确认收货并评价。
  expect((await buyer.call('POST', `/orders/${order.order_id}:confirm-receipt`)).status).toBe(200);
  const item = order.items[0];
  const review = await buyer.call('POST', `/orders/${order.order_id}/items/${item.order_item_id}:review`, {
    rating: 5,
    content: `光线柔和，适合晚上看书（${testInfo.project.name}）`,
    tags: ['护眼'],
  });
  expect(review.status).toBe(201);
  expect((await buyer.call('POST', `/orders/${order.order_id}/items/${item.order_item_id}:review`, { rating: 1, content: '再评一次' })).body.code).toBe(
    'review_exists',
  );

  // 商家详情页看到完整时间线和“已评价”。
  await page.goto(`/#/merchant/orders/${order.order_id}`);
  await expect(page.getByRole('heading', { name: `订单 ${order.order_no}` })).toBeVisible();
  await expect(page.locator('.doc-meta').first()).toContainText('已完成');
  await expect(page.getByText('发货时间')).toBeVisible();
  await expect(page.getByText('收货时间')).toBeVisible();
  await expect(page.getByText(/流水号 MOCK/)).toBeVisible();
  await expect(page.getByText('已评价')).toBeVisible();
  await expect(page.getByRole('button', { name: /发货/ })).toHaveCount(0);
  await page.screenshot({ path: shots(testInfo, 'merchant-detail'), fullPage: true });

  // 数码店商家看不到家居店的订单（只改 hash 不会重新加载页面，所以换账号后要刷新）。
  await signedIn(page, await apiLogin(request, 'blink_merchant'));
  await page.reload();
  await expect(page.getByRole('alert')).toContainText('订单不存在');
});

test('管理员取消待支付订单：库存回补；已支付订单只能发货', async ({ page, request }, testInfo) => {
  const buyer = await newBuyer(request, testInfo.project.name[0]);
  const before = await lampStock(request);
  expect((await buyer.call('POST', '/cart/items', { product_id: 'p_seed_lamp' })).status).toBe(200);
  const pending = (await buyer.call('POST', '/orders:checkout', { idempotency_key: `p-${Date.now()}` })).body.items[0];
  expect((await buyer.call('POST', '/cart/items', { product_id: 'p_seed_lamp' })).status).toBe(200);
  const toPay = (await buyer.call('POST', '/orders:checkout', { idempotency_key: `q-${Date.now()}` })).body.items[0];
  expect((await buyer.call('POST', `/orders/${toPay.order_id}:pay`)).status).toBe(200);
  expect(await lampStock(request)).toBe(before - 2);

  await signedIn(page, await apiLogin(request, 'blink_admin'));
  await page.goto('/#/admin/orders');
  await expect(page.getByRole('heading', { name: '订单管理' })).toBeVisible();
  await page.getByLabel('店铺').selectOption({ label: 'Blink 家居生活馆' });
  await page.getByLabel('订单号').fill(pending.order_no);
  await page.getByRole('button', { name: '查找' }).click();
  const row = page.getByRole('listitem').filter({ hasText: pending.order_no });
  await expect(row.locator('.status')).toHaveText('待支付');
  await expect(row).toContainText('Blink 家居生活馆');
  await expect(row.getByRole('button', { name: /发货/ })).toHaveCount(0);
  await row.getByRole('button', { name: `取消订单 ${pending.order_no}` }).click();
  await expect(page.getByRole('dialog')).toContainText('库存回补');
  await page.screenshot({ path: shots(testInfo, 'admin-cancel'), fullPage: true });
  await page.getByRole('dialog').getByRole('button', { name: '确认取消' }).click();
  await expect(page.locator('.flash')).toContainText(`订单 ${pending.order_no} 已取消，库存已回补`);
  await expect(row.locator('.status')).toHaveText('已取消');
  expect(await lampStock(request)).toBe(before - 1);
  const closed = (await buyer.call('GET', `/orders/${pending.order_id}`)).body;
  expect(closed).toMatchObject({ status: 'cancelled', cancel_reason: '平台取消' });
  expect(closed.payment.status).toBe('closed');

  // 已支付的订单：没有取消按钮，可以代发货。
  await page.getByLabel('订单号').fill(toPay.order_no);
  await page.getByRole('button', { name: '查找' }).click();
  const paidRow = page.getByRole('listitem').filter({ hasText: toPay.order_no });
  await expect(paidRow.locator('.status')).toHaveText('待发货');
  await expect(paidRow.getByRole('button', { name: /取消订单/ })).toHaveCount(0);
  await paidRow.getByRole('button', { name: `发货 ${toPay.order_no}` }).click();
  await page.getByRole('dialog').getByRole('button', { name: '确认发货' }).click();
  await expect(paidRow.locator('.status')).toHaveText('已发货');
  await expect(page.locator('.flash')).toContainText(`订单 ${toPay.order_no} 已发货`);
  expect((await buyer.call('GET', `/orders/${toPay.order_id}`)).body.status).toBe('shipped');
});

test('订单接口：并发结算不超卖、越权 404、状态冲突 409、商家与管理员页面互不可见', async ({ page, request }, testInfo) => {
  // 6 个买家同时买最后几件：先把库存调到 3 件（商家改库存），结束后恢复。
  const merchant = await apiLogin(request, 'blink_merchant2');
  const auth = { Authorization: `Bearer ${merchant.token}` };
  const product = await (await request.get('/api/v1/merchant/products/p_seed_lamp', { headers: auth })).json();
  const original = product.skus.map((s) => ({ ...s }));
  const setStock = async (n) => {
    const skus = original.map((s) => ({ sku_id: s.sku_id, sku_name: s.sku_name, price: s.price, stock_quantity: s.sku_id === 'sku_seed_lamp_white' ? n : s.stock_quantity, specs: s.specs, is_default: s.is_default }));
    const res = await request.patch('/api/v1/merchant/products/p_seed_lamp', { headers: auth, data: { skus } });
    expect(res.status(), await res.text()).toBe(200);
  };
  const buyers = [];
  for (let i = 0; i < 6; i++) {
    const b = await newBuyer(request, `${testInfo.project.name[0]}${i}`);
    expect((await b.call('POST', '/cart/items', { product_id: 'p_seed_lamp' })).status).toBe(200);
    buyers.push(b);
  }
  await setStock(3);
  try {
    const results = await Promise.all(buyers.map((b, i) => b.call('POST', '/orders:checkout', { idempotency_key: `race-${i}` })));
    const codes = results.map((r) => r.status).sort();
    expect(codes).toEqual([201, 201, 201, 409, 409, 409]);
    expect(results.filter((r) => r.status === 409).every((r) => r.body.code === 'item_unavailable')).toBe(true);
    expect(await lampStock(request)).toBe(0);

    // 别人的订单 404；状态不对 409。
    const winner = results.findIndex((r) => r.status === 201);
    const loser = results.findIndex((r) => r.status === 409);
    const order = results[winner].body.items[0];
    expect((await buyers[loser].call('GET', `/orders/${order.order_id}`)).body.code).toBe('order_not_found');
    expect((await buyers[loser].call('POST', `/orders/${order.order_id}:pay`)).body.code).toBe('order_not_found');
    expect((await buyers[winner].call('POST', `/orders/${order.order_id}:confirm-receipt`)).body.code).toBe('order_status_conflict');
    expect((await buyers[winner].call('POST', `/orders/${order.order_id}:cancel`)).status).toBe(200);
    expect(await lampStock(request)).toBe(1);
  } finally {
    await setStock(original.find((s) => s.sku_id === 'sku_seed_lamp_white').stock_quantity);
  }

  // 商家打不开管理员订单页，买家接口商家调用 403。
  await signedIn(page, merchant);
  await page.goto('/#/admin/orders');
  await expect(page.getByText('当前账号不是管理员，无权访问平台管理')).toBeVisible();
  const res = await request.get('/api/v1/orders', { headers: auth });
  expect(res.status()).toBe(403);
});
