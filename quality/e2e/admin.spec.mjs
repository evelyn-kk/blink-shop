import { expect, test } from '@playwright/test';

// 平台管理与风控：真实 API。对共享数据（店铺状态、风险词）的修改都在 finally 里恢复，不影响其他用例。

const PASSWORD = 'BlinkDev#2026';
const shots = (testInfo, name) => `reports/e2e/screens/${testInfo.project.name}-admin-${name}.png`;

async function apiLogin(request, username, password = PASSWORD) {
  const res = await request.post('/api/v1/auth/login', { data: { username, password } });
  expect(res.ok(), `login ${username}`).toBeTruthy();
  return res.json();
}

async function signedIn(page, session) {
  await page.addInitScript((s) => window.localStorage.setItem('blink_shop.session', JSON.stringify(s)), session);
}

test('账号风控：原因必填 → 该账号立即只读 → 操作记录可查 → 恢复正常', async ({ page, request }, testInfo) => {
  const username = `adm_${testInfo.project.name[0]}_${String(Date.now()).slice(-8)}`;
  const reg = await request.post('/api/v1/auth/register', { data: { username, password: 'E2e-Admin#2026', display_name: '风控测试' } });
  expect(reg.status()).toBe(201);
  const victim = await reg.json();
  const addToCart = () =>
    request.post('/api/v1/cart/items', { headers: { Authorization: `Bearer ${victim.token}` }, data: { product_id: 'p_seed_lamp' } });

  await signedIn(page, await apiLogin(request, 'blink_admin'));
  await page.goto('/#/admin/platform');
  await expect(page.getByRole('heading', { name: '平台管理' })).toBeVisible();
  // 查找后地址更新、列表重新加载，等出现目标行再继续（查找框会随地址重新创建）。
  const search = async (keyword) => {
    await page.getByLabel('按账号或名称查找').fill(keyword);
    await page.getByRole('button', { name: '查找' }).click();
    await expect(page).toHaveURL(new RegExp(`keyword=${keyword}`));
    await expect(page.getByRole('row').filter({ hasText: keyword })).toHaveCount(1);
  };
  await search(username);
  const row = page.getByRole('row').filter({ hasText: username });
  await expect(row.locator('.status')).toHaveText('正常');
  // 自己的账号没有状态按钮。
  await search('blink_admin');
  await expect(page.getByRole('row').filter({ hasText: 'blink_admin' }).getByRole('link', { name: /操作记录/ })).toBeVisible();
  await expect(page.getByRole('row').filter({ hasText: 'blink_admin' }).getByRole('button')).toHaveCount(0);
  await search(username);

  await row.getByRole('button', { name: `设为风控 ${username}` }).click();
  const dialog = page.getByRole('dialog');
  await expect(dialog).toContainText('风控后该账号只能浏览');
  await dialog.getByRole('button', { name: '确认设为风控' }).click();
  await expect(dialog.getByText('请填写原因')).toBeVisible();
  await dialog.getByLabel('原因').fill('E2E：疑似刷单');
  await page.screenshot({ path: shots(testInfo, 'reason'), fullPage: true });
  await dialog.getByRole('button', { name: '确认设为风控' }).click();
  await expect(page.locator('.flash').last()).toContainText(`「${username}」已设为风控`);
  await expect(row.locator('.status')).toHaveText('风控');
  expect((await addToCart()).status()).toBe(403);

  await row.getByRole('link', { name: `查看 ${username} 的操作记录` }).click();
  await expect(page.getByRole('heading', { name: '操作审计' })).toBeVisible();
  const audit = page.getByRole('row').filter({ hasText: 'E2E：疑似刷单' });
  await expect(audit).toContainText('account.status_changed');
  await expect(audit).toContainText('active → risk');
  await expect(audit).toContainText('Blink 平台管理员');
  await page.screenshot({ path: shots(testInfo, 'audit'), fullPage: true });

  await page.goBack();
  await row.getByRole('button', { name: `恢复正常 ${username}` }).click();
  await expect(page.getByRole('dialog').getByLabel('原因')).toHaveCount(0); // 恢复不需要原因
  await page.getByRole('dialog').getByRole('button', { name: '确认恢复正常' }).click();
  await expect(row.locator('.status')).toHaveText('正常');
  expect((await addToCart()).status()).toBe(200);
});

test('店铺停业：商品立即对外隐藏，风控页计数变化，恢复后回来', async ({ page, request }, testInfo) => {
  const admin = await apiLogin(request, 'blink_admin');
  const restore = () =>
    request.patch('/api/v1/admin/merchants/m_seed_home', { headers: { Authorization: `Bearer ${admin.token}` }, data: { status: 'active' } });
  await signedIn(page, admin);
  await page.goto('/#/admin/platform?tab=merchants');
  const row = page.getByRole('row').filter({ hasText: 'Blink 家居生活馆' });
  try {
    await row.getByRole('button', { name: '停用 Blink 家居生活馆' }).click();
    await expect(page.getByRole('dialog')).toContainText('都会立即对外隐藏');
    await page.getByRole('dialog').getByLabel('原因').fill('E2E：资质复核');
    await page.getByRole('dialog').getByRole('button', { name: '确认停用' }).click();
    await expect(row.locator('.status')).toHaveText('停用');
    expect((await request.get('/api/v1/products/p_seed_lamp')).status()).toBe(404);

    await page.getByRole('navigation', { name: '主导航' }).getByRole('link', { name: '风控' }).click();
    await expect(page.getByRole('heading', { name: '风控' })).toBeVisible();
    await expect(page.getByRole('link', { name: '停用的店铺：1' })).toBeVisible();
    await page.screenshot({ path: shots(testInfo, 'risk'), fullPage: true });
    await page.getByRole('link', { name: '停用的店铺：1' }).click();
    await expect(page).toHaveURL(/#\/admin\/platform\?tab=merchants&status=inactive$/);
    await expect(page.getByRole('row').filter({ hasText: 'Blink 家居生活馆' })).toHaveCount(1);
  } finally {
    await restore();
  }
  expect((await request.get('/api/v1/products/p_seed_lamp')).status()).toBe(200);
});

test('风险词与配置：密钥只显示掩码；风险词保存后立即生效并有审计', async ({ page, request }, testInfo) => {
  const admin = await apiLogin(request, 'blink_admin');
  const auth = { Authorization: `Bearer ${admin.token}` };
  await signedIn(page, admin);
  try {
    await page.goto('/#/admin/risk');
    const words = page.getByRole('textbox', { name: '风险词' });
    await expect(words).toHaveValue(/违法/);
    await words.fill(`违法\n刷单\n${'长'.repeat(21)}`);
    await page.getByRole('button', { name: '保存风险词' }).click();
    await expect(page.getByText('每个风险词最多 20 个字')).toBeVisible();
    await words.fill('违法\n刷单\n刷单\n套现');
    await page.getByRole('button', { name: '保存风险词' }).click();
    await expect(page.locator('.flash').last()).toContainText('风险词已保存');
    const overview = await (await request.get('/api/v1/admin/risk/overview', { headers: auth })).json();
    expect(overview.blocked_words).toEqual(['违法', '刷单', '套现']);
    expect(overview.blocked_words_source).toBe('dynamic');
    await expect(page.getByText('最近操作')).toBeVisible();
    await expect(page.locator('.doc-row').filter({ hasText: 'config.updated' }).first()).toContainText('risk.blocked_words');

    await page.goto('/#/admin/configs');
    await expect(page.getByRole('heading', { name: '应用配置' })).toBeVisible();
    const body = await page.locator('main').innerText();
    expect(body).not.toContain('minioadmin');
    expect(body).not.toContain('blink_dev');
    const secret = page.getByLabel('minio.secret_key');
    await expect(secret).toHaveValue('******');
    await expect(secret).toHaveAttribute('readonly', '');
    await page.screenshot({ path: shots(testInfo, 'configs'), fullPage: true });
  } finally {
    await request.patch('/api/v1/admin/configs/risk.blocked_words', { headers: auth, data: { value: '' } });
  }
});

test('商家和普通用户打不开管理页面，也调不了管理接口', async ({ page, request }) => {
  for (const username of ['blink_merchant', 'blink_user']) {
    const session = await apiLogin(request, username);
    await signedIn(page, session);
    for (const path of ['/#/admin/platform', '/#/admin/risk', '/#/admin/configs', '/#/admin/audit']) {
      await page.goto(path);
      await page.reload();
      await expect(page.getByText('当前账号不是管理员，无权访问平台管理')).toBeVisible();
    }
    const res = await request.get('/api/v1/admin/configs', { headers: { Authorization: `Bearer ${session.token}` } });
    expect(res.status()).toBe(403);
  }
});
