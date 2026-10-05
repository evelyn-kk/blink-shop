import { expect, test } from '@playwright/test';

// 商家工作台：促销（新建 → 参与计价 → 编辑 → 停用）、评价回复、列表刷新。真实 API。
// 促销门槛设为 5000 元：其他用例最多买 2 件台灯，不会碰到它；用例结束时还会停用。

const PASSWORD = 'BlinkDev#2026';
const shots = (testInfo, name) => `reports/e2e/screens/${testInfo.project.name}-merchant-ops-${name}.png`;
const uniq = (testInfo) => `${testInfo.project.name[0]}${String(Date.now()).slice(-7)}`;

async function apiLogin(request, username) {
  const res = await request.post('/api/v1/auth/login', { data: { username, password: PASSWORD } });
  expect(res.ok(), `login ${username}`).toBeTruthy();
  return res.json();
}

async function signedIn(page, session) {
  await page.addInitScript((s) => window.localStorage.setItem('blink_shop.session', JSON.stringify(s)), session);
}

function client(request, token) {
  return async (method, path, data, headers = {}) => {
    const r = await request.fetch(`/api/v1${path}`, { method, data, headers: { Authorization: `Bearer ${token}`, ...headers } });
    return { status: r.status(), body: await r.json() };
  };
}

async function newBuyer(request, tag) {
  const username = `mo_${tag}_${Math.floor(Math.random() * 1e6)}`;
  const res = await request.post('/api/v1/auth/register', { data: { username, password: 'E2e-Ops#2026', display_name: '运营测试' } });
  expect(res.status(), await res.text()).toBe(201);
  return client(request, (await res.json()).token);
}

test('促销：新建后参与计价，编辑金额，停用后不再生效；连点只创建一个；看不到其他店铺的促销', async ({ page, request }, testInfo) => {
  const merchant = await apiLogin(request, 'blink_merchant2');
  const api = client(request, merchant.token);
  const name = `E2E 大件满减 ${uniq(testInfo)}`;
  const buyer = await newBuyer(request, testInfo.project.name[0]);
  // 同一个库反复跑时台灯库存会被其他用例的订单用掉；不够 21 件就由店铺先补到 60 件。
  const lamp = (await api('GET', '/merchant/products/p_seed_lamp')).body;
  const white = lamp.skus.find((k) => k.sku_id === 'sku_seed_lamp_white');
  if (white.stock_quantity < 21) {
    const skus = lamp.skus.map((k) => ({ sku_id: k.sku_id, sku_name: k.sku_name, price: k.price, specs: k.specs, is_default: k.is_default,
      stock_quantity: k.sku_id === white.sku_id ? 60 : k.stock_quantity }));
    expect((await api('PATCH', '/merchant/products/p_seed_lamp', { skus })).status).toBe(200);
  }
  expect((await buyer('POST', '/cart/items', { product_id: 'p_seed_lamp', quantity: 21 })).status).toBe(200); // 5229 元
  const lineOf = async () => {
    const preview = (await buyer('GET', '/cart/discount-preview?user_coupon_ids=')).body;
    return preview.lines.find((l) => l.name === name);
  };

  await signedIn(page, merchant);
  await page.goto('/#/merchant/products');
  await page.getByRole('navigation', { name: '主导航' }).getByRole('link', { name: '促销' }).click();
  await expect(page.getByRole('heading', { name: '促销', exact: true })).toBeVisible();
  await expect(page.getByText('Blink 数码满 1000 减 80')).toHaveCount(0); // 数码店的促销
  await page.getByRole('link', { name: '新建促销' }).click();

  // 本地校验：定位到第一个出错字段。
  await page.getByRole('button', { name: '创建促销' }).click();
  await expect(page.getByText('请填写促销名称')).toBeVisible();
  await expect(page.getByLabel('名称')).toBeFocused();

  await page.getByLabel('名称').fill(name);
  await page.getByLabel('门槛（元）').fill('5000');
  await page.getByLabel('减免金额（元）').fill('300');
  await page.screenshot({ path: shots(testInfo, 'form'), fullPage: true });
  // 连点两次只创建一个。
  const submit = page.getByRole('button', { name: '创建促销' });
  await submit.dblclick();
  await expect(page).toHaveURL(/#\/merchant\/promotions$/);
  await expect(page.locator('.flash').last()).toContainText(`已创建「${name}」`);
  const row = page.getByRole('row').filter({ hasText: name });
  await expect(row).toHaveCount(1);
  await expect(row).toContainText('满 5000 减 300');
  await expect(row).toContainText('全店');
  await expect(row.locator('.status')).toHaveText('进行中');
  await page.screenshot({ path: shots(testInfo, 'list'), fullPage: true });
  const listed = (await api('GET', '/merchant/promotions?page_size=100')).body.items.filter((p) => p.name === name);
  expect(listed).toHaveLength(1);
  const id = listed[0].promotion_id;

  try {
    expect(await lineOf()).toMatchObject({ amount: '300.00', description: '满 5000 减 300' });

    // 编辑：改减免金额，其他不变。
    await row.getByRole('link', { name: `编辑 ${name}` }).click();
    await expect(page.getByRole('heading', { name: '编辑促销' })).toBeVisible();
    await expect(page.getByLabel('名称')).toHaveValue(name);
    await expect(page.getByLabel('门槛（元）')).toHaveValue('5000');
    await page.getByLabel('减免金额（元）').fill('6000');
    await page.getByRole('button', { name: '保存修改' }).click();
    await expect(page.getByLabel('减免金额（元）')).toHaveAttribute('aria-invalid', 'true');
    await expect(page.getByText('减免金额不能超过门槛')).toBeVisible();
    await page.getByLabel('减免金额（元）').fill('350');
    await page.getByRole('button', { name: '保存修改' }).click();
    await expect(page.locator('.flash').last()).toContainText(`已保存「${name}」`);
    await expect(row).toContainText('满 5000 减 350');
    expect((await lineOf()).amount).toBe('350.00');

    // 停用需要确认；停用后立即不参与计价，可以再启用。
    await row.getByRole('button', { name: `停用 ${name}` }).click();
    await expect(page.getByRole('dialog')).toContainText('停用后立即不再参与计价');
    await page.getByRole('dialog').getByRole('button', { name: '确认停用' }).click();
    await expect(page.locator('.flash').last()).toContainText(`已停用「${name}」`);
    await expect(row.locator('.status')).toHaveText('已停用');
    expect(await lineOf()).toBeUndefined();
    await page.getByRole('link', { name: '已停用' }).click();
    await expect(page.getByRole('row').filter({ hasText: name })).toHaveCount(1);
  } finally {
    await api('PATCH', `/merchant/promotions/${id}`, { status: 'inactive' });
  }
});

test('评价：未回复列表 → 回复（空回复被拦下）→ 公开评价可见 → 修改回复；其他店铺看不到', async ({ page, request }, testInfo) => {
  // 买家走完下单到评价，产生一条待回复的评价。用台灯：目录用例断言了鼠标只有一条种子评价。
  const buyer = await newBuyer(request, testInfo.project.name[0]);
  const merchant = await apiLogin(request, 'blink_merchant2');
  const merchantApi = client(request, merchant.token);
  expect((await buyer('POST', '/cart/items', { product_id: 'p_seed_lamp' })).status).toBe(200);
  const order = (await buyer('POST', '/orders:checkout', { idempotency_key: `rv-${Date.now()}` })).body.items[0];
  expect((await buyer('POST', `/orders/${order.order_id}:pay`)).status).toBe(200);
  expect((await merchantApi('PATCH', `/merchant/orders/${order.order_id}`, { status: 'shipped' })).status).toBe(200);
  expect((await buyer('POST', `/orders/${order.order_id}:confirm-receipt`)).status).toBe(200);
  const content = `光线柔和 ${uniq(testInfo)}`;
  const review = (await buyer('POST', `/orders/${order.order_id}/items/${order.items[0].order_item_id}:review`, { rating: 4, content, tags: ['护眼'] })).body;

  await signedIn(page, merchant);
  await page.goto('/#/merchant/reviews');
  await page.getByRole('link', { name: '未回复' }).click();
  const item = page.getByRole('article').filter({ hasText: content });
  await expect(item).toBeVisible();
  await expect(item.getByLabel('4 分（满分 5 分）')).toBeVisible();
  await expect(item).toContainText('运***');
  await item.getByRole('button', { name: '提交回复' }).click();
  await expect(item.getByRole('alert')).toHaveText('回复内容需要 1 到 500 个字');
  await item.getByLabel('回复').fill('感谢支持，护眼模式晚上更舒服');
  await page.screenshot({ path: shots(testInfo, 'reply'), fullPage: true });
  await item.getByRole('button', { name: '提交回复' }).click();
  await expect(page.locator('.flash').last()).toContainText('已回复「Blink 护眼台灯 L1」的评价');
  await expect(page.getByRole('article').filter({ hasText: content })).toHaveCount(0); // 已不在“未回复”里

  const reviews = (await (await request.get('/api/v1/products/p_seed_lamp/reviews?page_size=100')).json()).items;
  expect(reviews.find((r) => r.review_id === review.review_id).merchant_reply).toBe('感谢支持，护眼模式晚上更舒服');

  await page.getByRole('link', { name: '已回复' }).click();
  const replied = page.getByRole('article').filter({ hasText: content });
  await expect(replied).toContainText('商家回复：感谢支持，护眼模式晚上更舒服');
  await replied.getByRole('button', { name: '修改回复' }).click();
  await replied.getByLabel('修改回复').fill('已补发一个灯罩');
  await replied.getByRole('button', { name: '提交回复' }).click();
  await expect(page.getByRole('article').filter({ hasText: content })).toContainText('商家回复：已补发一个灯罩');

  // 数码店看不到家居店商品的评价。
  await signedIn(page, await apiLogin(request, 'blink_merchant'));
  await page.reload();
  await expect(page.getByRole('heading', { name: '评价', exact: true })).toBeVisible();
  await expect(page.getByRole('article').filter({ hasText: content })).toHaveCount(0);
});

test('列表“刷新”按钮：页面打开后别处产生的新订单，刷新后出现', async ({ page, request }, testInfo) => {
  const merchant = await apiLogin(request, 'blink_merchant2');
  await signedIn(page, merchant);
  await page.goto('/#/merchant/orders?status=pending_payment');
  await expect(page.getByRole('heading', { name: '订单', exact: true })).toBeVisible();
  const buyer = await newBuyer(request, testInfo.project.name[0]);
  expect((await buyer('POST', '/cart/items', { product_id: 'p_seed_lamp' })).status).toBe(200);
  const order = (await buyer('POST', '/orders:checkout', { idempotency_key: `rf-${Date.now()}` })).body.items[0];
  await expect(page.getByText(order.order_no)).toHaveCount(0);
  await page.getByRole('button', { name: '刷新' }).click();
  await expect(page.getByText(`订单 ${order.order_no}`)).toBeVisible();
  await buyer('POST', `/orders/${order.order_id}:cancel`);
});
