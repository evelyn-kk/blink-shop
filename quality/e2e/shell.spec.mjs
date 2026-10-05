import { expect, test } from '@playwright/test';

// Web 骨架：登录、会话失效、角色路由、全局通知、键盘操作和窄屏。真实 API。

const PASSWORD = 'BlinkDev#2026';
const shots = (testInfo, name) => `reports/e2e/screens/${testInfo.project.name}-shell-${name}.png`;

async function apiLogin(request, username) {
  const res = await request.post('/api/v1/auth/login', { data: { username, password: PASSWORD } });
  expect(res.ok(), `login ${username}`).toBeTruthy();
  return res.json();
}

async function signedIn(page, session) {
  await page.addInitScript((s) => window.localStorage.setItem('blink_shop.session', JSON.stringify(s)), session);
}

test('只用键盘登录：校验定位到字段，焦点清晰可见，登录后回到原页面，刷新后仍是登录状态', async ({ page }, testInfo) => {
  await page.goto('/#/merchant/orders?status=paid');
  await expect(page).toHaveURL(/#\/login\?next=/);
  const username = page.getByLabel('账号');
  await username.focus();
  await page.keyboard.press('Enter');
  await expect(page.getByText('请输入账号')).toBeVisible();
  await expect(page.getByText('请输入密码')).toBeVisible();
  await expect(username).toBeFocused();
  await expect(username).toHaveAttribute('aria-invalid', 'true');
  // 键盘焦点有 2px 实线焦点框。
  const outline = await username.evaluate((el) => {
    const s = getComputedStyle(el);
    return `${s.outlineStyle} ${s.outlineWidth}`;
  });
  expect(outline).toBe('solid 2px');
  await page.screenshot({ path: shots(testInfo, 'login-errors'), fullPage: true });

  await page.keyboard.type('blink_merchant2');
  await page.keyboard.press('Tab');
  await expect(page.getByLabel('密码')).toBeFocused();
  await page.keyboard.type('wrong-password');
  await page.keyboard.press('Enter');
  await expect(page.getByRole('alert')).toContainText('账号或密码错误');
  await expect(page.getByLabel('密码')).toBeFocused();
  await page.keyboard.type(PASSWORD); // 密码已被选中，直接重新输入
  await page.keyboard.press('Enter');

  await expect(page).toHaveURL(/#\/merchant\/orders\?status=paid$/);
  await expect(page.getByRole('heading', { name: '订单', exact: true })).toBeVisible();
  await expect(page.getByRole('link', { name: '待发货' })).toHaveAttribute('aria-current', 'page');
  await page.reload();
  await expect(page.getByRole('heading', { name: '订单', exact: true })).toBeVisible();
  await expect(page.getByRole('banner')).toContainText('Blink 家居运营');
  await page.screenshot({ path: shots(testInfo, 'merchant-home'), fullPage: true });
});

test('token 在服务端被撤销：提示登录已失效并回到登录页，重新登录后回到原页面', async ({ page, request }, testInfo) => {
  const session = await apiLogin(request, 'blink_admin');
  await signedIn(page, session);
  await page.goto('/#/admin/orders');
  await expect(page.getByRole('heading', { name: '订单管理' })).toBeVisible();
  // 等页面的请求（订单、店铺下拉）都完成，再撤销 token：否则还没发出的请求会先拿到 401，页面提前回到登录页。
  await page.waitForLoadState('networkidle');

  // 在别处退出（撤销 token），本页再发请求时返回 401。
  const out = await request.post('/api/v1/auth/logout', { headers: { Authorization: `Bearer ${session.token}` } });
  expect(out.ok()).toBeTruthy();
  await page.getByRole('link', { name: '待支付' }).click();
  await expect(page).toHaveURL(/#\/login\?next=%23%2Fadmin%2Forders/);
  const notice = page.getByRole('status').filter({ hasText: '登录已失效，请重新登录' });
  await expect(notice).toBeVisible();
  expect(await page.evaluate(() => window.localStorage.getItem('blink_shop.session'))).toBeNull();
  await page.screenshot({ path: shots(testInfo, 'expired-notice'), fullPage: true });

  // 通知可以用键盘关闭。
  await notice.getByRole('button', { name: '关闭提示' }).focus();
  await page.keyboard.press('Enter');
  await expect(notice).toHaveCount(0);

  await page.getByLabel('账号').fill('blink_admin');
  await page.getByLabel('密码').fill(PASSWORD);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/admin\/orders\?status=pending_payment$/);
  await expect(page.getByRole('heading', { name: '订单管理' })).toBeVisible();
});

test('页面一直开着时 token 到期：自动退出并提示', async ({ page, request }) => {
  const session = await apiLogin(request, 'blink_merchant');
  await signedIn(page, { ...session, expires_at: new Date(Date.now() + 4000).toISOString() });
  await page.goto('/#/merchant/products');
  await expect(page.getByRole('heading', { name: '我的商品' })).toBeVisible();
  await expect(page).toHaveURL(/#\/login\?next=%23%2Fmerchant%2Fproducts/, { timeout: 10_000 });
  await expect(page.getByRole('status').filter({ hasText: '登录已失效，请重新登录' })).toBeVisible();
  await expect(page.getByRole('banner').getByRole('link', { name: '登录' })).toBeVisible();
});

test('按角色显示导航，越权页面给出提示；已登录访问登录页直接进入默认页面', async ({ page, request }) => {
  await signedIn(page, await apiLogin(request, 'blink_user'));
  await page.goto('/#/login');
  await expect(page).toHaveURL(/#\/products$/);
  const nav = page.getByRole('navigation', { name: '主导航' });
  await expect(nav.getByRole('link')).toHaveText(['商品巡检']);
  await page.goto('/#/admin/orders');
  await expect(page.getByText('当前账号不是管理员，无权访问平台管理')).toBeVisible();
  await page.goto('/#/merchant/orders');
  await expect(page.getByText('当前账号不是商家，无权访问商家工作台')).toBeVisible();
});

test('全局通知跨页面保留：保存商品后回到列表仍能看到结果', async ({ page, request }, testInfo) => {
  await signedIn(page, await apiLogin(request, 'blink_merchant'));
  await page.goto('/#/merchant/products');
  await page.getByRole('link', { name: /编辑 Blink 静音无线鼠标/ }).click();
  await expect(page.getByLabel('商品名称')).toHaveValue('Blink 静音无线鼠标 M2');
  await page.getByRole('button', { name: '保存修改' }).click();
  await expect(page).toHaveURL(/#\/merchant\/products$/);
  const notice = page.getByRole('status').filter({ hasText: '已保存「Blink 静音无线鼠标 M2」' });
  await expect(notice).toBeVisible();
  await page.screenshot({ path: shots(testInfo, 'notice'), fullPage: true });
  await expect(notice).toHaveCount(0, { timeout: 8000 }); // 5 秒后自动消失
});
