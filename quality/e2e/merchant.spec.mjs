import { expect, test } from '@playwright/test';

// 商家工作台：真实 API。每个用例创建的商品名称都带 E2E 前缀，并在结束后删除，不影响目录用例的商品数量。

const MERCHANT = { username: 'blink_merchant', password: 'BlinkDev#2026' };
const flash = (page) => page.locator('.flash');
const shots = (testInfo, name) => `reports/e2e/screens/${testInfo.project.name}-merchant-${name}.png`;

async function apiLogin(request, user = MERCHANT) {
  const res = await request.post('/api/v1/auth/login', { data: user });
  expect(res.ok(), `login ${user.username}`).toBeTruthy();
  return res.json();
}

async function api(request, token, method, path, data) {
  return request.fetch(`/api/v1${path}`, { method, data, headers: { Authorization: `Bearer ${token}` } });
}

// 把会话写入 localStorage 后再打开页面，省去每个用例都走一遍登录界面。
async function signedIn(page, session) {
  await page.addInitScript((s) => window.localStorage.setItem('blink_shop.session', JSON.stringify(s)), session);
}

let session;
test.beforeAll(async ({ request }) => {
  session = await apiLogin(request);
});

test.afterEach(async ({ request }) => {
  const res = await api(request, session.token, 'GET', '/merchant/products?keyword=E2E&page_size=100');
  for (const p of (await res.json()).items) {
    await api(request, session.token, 'DELETE', `/merchant/products/${p.product_id}`);
  }
});

const uniqueName = (testInfo, label) => `E2E ${label} ${testInfo.project.name} ${Date.now()}`;

async function publicCount(request, keyword) {
  const res = await request.get(`/api/v1/products?keyword=${encodeURIComponent(keyword)}&page_size=100`);
  const body = await res.json();
  return body.items.filter((p) => p.name === keyword).length;
}

async function merchantCount(request, keyword) {
  const res = await api(request, session.token, 'GET', `/merchant/products?keyword=${encodeURIComponent(keyword)}`);
  return (await res.json()).total;
}

test('未登录跳转登录，登录后回到工作台；非商家无权访问', async ({ page }, testInfo) => {
  await page.goto('/#/merchant/products');
  await expect(page).toHaveURL(/#\/login\?next=/);
  await expect(page.getByRole('heading', { name: '登录' })).toBeVisible();

  await page.getByLabel('账号').fill(MERCHANT.username);
  await page.getByLabel('密码').fill('wrong-password');
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page.getByRole('alert')).toContainText('账号或密码错误');

  await page.getByLabel('密码').fill(MERCHANT.password);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/merchant\/products$/);
  await expect(page.getByRole('heading', { name: '我的商品' })).toBeVisible();
  // 本店未删除的 7 个商品，含下架和风控。
  await expect(page.getByText('共 7 件')).toBeVisible();
  await expect(page.getByText('风控审核中').first()).toBeVisible();
  await page.screenshot({ path: shots(testInfo, 'list'), fullPage: true });

  await page.getByRole('button', { name: '退出' }).click();
  // 等退出完成（回到商品列表、顶栏出现“登录”）后再去登录页，避免与退出后的跳转交错。
  await expect(page.getByRole('banner').getByRole('link', { name: '登录' })).toBeVisible();
  await page.goto('/#/login');
  await page.getByLabel('账号').fill('blink_user');
  await page.getByLabel('密码').fill(MERCHANT.password);
  await page.getByRole('button', { name: '登录' }).click();
  // 普通用户登录后回到商品列表；等登录完成再访问商家页。
  await expect(page.getByRole('banner')).toContainText('演示用户');
  await page.goto('/#/merchant/products');
  await expect(page.getByText('当前账号不是商家，无权访问商家工作台')).toBeVisible();
});

test('新建 → 公开可见 → 编辑下架 → 删除（二次确认）', async ({ page, request }, testInfo) => {
  await signedIn(page, session);
  const name = uniqueName(testInfo, '键盘');
  await page.goto('/#/merchant/products');
  await page.getByRole('link', { name: '新建商品' }).click();
  await expect(page.getByRole('heading', { name: '新建商品' })).toBeVisible();

  await page.getByLabel('商品名称').fill(name);
  await page.getByLabel('分类').selectOption('c_keyboard');
  await page.getByLabel('规格名称').fill('白色');
  await page.getByLabel('价格（元）').fill('88');
  await page.getByLabel('库存').fill('20');
  await page.getByRole('button', { name: '添加规格' }).click();
  const second = page.getByRole('group', { name: '规格 2' });
  await second.getByLabel('规格名称').fill('黑色');
  await second.getByLabel('价格（元）').fill('99.5');
  await second.getByLabel('库存').fill('0');
  await second.getByLabel('属性').fill('颜色=黑；轴体=茶轴');
  await page.getByLabel('标签').fill('轻薄，静音');
  await page.getByLabel('图片地址').fill('/api/v1/assets/catalog/products/p_seed_keyboard.png');
  await page.screenshot({ path: shots(testInfo, 'form'), fullPage: true });
  await page.getByRole('button', { name: '创建商品' }).click();

  await expect(page.locator('.flash')).toContainText(`已创建「${name}」`);
  const row = page.getByRole('listitem').filter({ hasText: name });
  await expect(row).toContainText('上架中');
  await expect(row).toContainText('2 个规格');
  await expect(row).toContainText('¥88.00');
  expect(await publicCount(request, name)).toBe(1);

  await row.getByRole('link', { name: `编辑 ${name}` }).click();
  await expect(page.getByRole('heading', { name: '编辑商品' })).toBeVisible();
  await expect(page.getByLabel('商品名称')).toHaveValue(name);
  // 规格属性是对象，服务端按键排序返回，只核对内容。
  const specs = await page.getByRole('group', { name: '规格 2' }).getByLabel('属性').inputValue();
  expect(specs.split('；').sort()).toEqual(['轴体=茶轴', '颜色=黑'].sort());
  await page.getByLabel('状态').selectOption('inactive');
  await page.getByRole('button', { name: '保存修改' }).click();
  await expect(page.locator('.flash')).toContainText(`已保存「${name}」`);
  await expect(page.getByRole('listitem').filter({ hasText: name })).toContainText('已下架');
  expect(await publicCount(request, name)).toBe(0);

  // 删除：先取消，商品仍在；再确认，商品消失。
  await page.getByRole('button', { name: `删除 ${name}` }).click();
  const dialog = page.getByRole('dialog', { name: '删除商品' });
  await expect(dialog).toContainText(name);
  await page.screenshot({ path: shots(testInfo, 'delete-dialog') });
  await dialog.getByRole('button', { name: '取消' }).click();
  await expect(dialog).toBeHidden();
  expect(await merchantCount(request, name)).toBe(1);

  await page.getByRole('button', { name: `删除 ${name}` }).click();
  await dialog.getByRole('button', { name: '确认删除' }).click();
  await expect(page.locator('.flash')).toContainText(`已删除「${name}」`);
  await expect(page.getByRole('listitem').filter({ hasText: name })).toHaveCount(0);
  expect(await merchantCount(request, name)).toBe(0);
});

test('必填与格式校验，服务端错误定位到字段', async ({ page, request }, testInfo) => {
  await signedIn(page, session);
  const name = uniqueName(testInfo, '校验');
  await page.goto('/#/merchant/products/new');
  await expect(page.getByRole('heading', { name: '新建商品' })).toBeVisible();
  await page.getByLabel('规格名称').fill('');
  await page.getByRole('button', { name: '创建商品' }).click();
  await expect(page.getByRole('alert').first()).toContainText('请先修正标出的字段');
  await expect(page.getByText('请填写商品名称')).toBeVisible();
  await expect(page.getByText('请选择分类')).toBeVisible();
  await expect(page.getByText('请填写规格名称')).toBeVisible();
  await expect(page.getByLabel('商品名称')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.getByLabel('商品名称')).toBeFocused();

  await page.getByLabel('商品名称').fill(name);
  await page.getByLabel('分类').selectOption('c_mouse');
  await page.getByLabel('规格名称').fill('默认款');
  await page.getByLabel('价格（元）').fill('1.234');
  await page.getByRole('button', { name: '创建商品' }).click();
  await expect(page.getByText('请填写大于 0 的价格，最多两位小数')).toBeVisible();
  // 出错后表单在下一帧把焦点移到第一个出错的字段；等焦点落定再输入，否则输入可能落到被抢走焦点的输入框里。
  await expect(page.getByLabel('价格（元）')).toBeFocused();

  // 客户端放行、服务端拒绝：市场价低于售价，错误显示在市场价字段下。
  await page.getByLabel('价格（元）').fill('50');
  await page.getByLabel('市场价（划线价）').fill('10');
  await page.getByRole('button', { name: '创建商品' }).click();
  await expect(page.getByRole('alert').first()).toContainText('市场价不能低于售价');
  await expect(page.getByLabel('市场价（划线价）')).toHaveAttribute('aria-invalid', 'true');
  await page.screenshot({ path: shots(testInfo, 'form-errors'), fullPage: true });
  expect(await merchantCount(request, name)).toBe(0);

  await page.getByLabel('市场价（划线价）').fill('');
  await page.getByRole('button', { name: '创建商品' }).click();
  await expect(page.locator('.flash')).toContainText(`已创建「${name}」`);
});

test('连点提交只创建一次', async ({ page, request }, testInfo) => {
  await signedIn(page, session);
  const name = uniqueName(testInfo, '连点');
  // 放慢创建请求，确保第二次点击发生在第一次请求返回之前。
  await page.route((url) => url.pathname === '/api/v1/merchant/products', async (route) => {
    if (route.request().method() === 'POST') await new Promise((r) => setTimeout(r, 600));
    await route.continue();
  });
  await page.goto('/#/merchant/products/new');
  await expect(page.getByRole('heading', { name: '新建商品' })).toBeVisible();
  await page.getByLabel('商品名称').fill(name);
  await page.getByLabel('分类').selectOption('c_mouse');
  await page.getByLabel('价格（元）').fill('19.9');
  const submit = page.getByRole('button', { name: '创建商品' });
  await submit.click();
  await expect(page.getByRole('button', { name: '保存中…' })).toBeDisabled();
  await page.getByRole('button', { name: '保存中…' }).click({ force: true }).catch(() => {});
  await page.locator('form').evaluate((f) => f.requestSubmit());
  await expect(page.locator('.flash')).toContainText(`已创建「${name}」`);
  expect(await merchantCount(request, name)).toBe(1);
});

test('取消编辑：有修改时二次确认，放弃后数据不变', async ({ page, request }, testInfo) => {
  const name = uniqueName(testInfo, '取消');
  const created = await (
    await api(request, session.token, 'POST', '/merchant/products', {
      name, category_id: 'c_mouse', price: '30', stock_quantity: 3,
    })
  ).json();
  await signedIn(page, session);

  // 没有修改：直接返回列表。
  await page.goto(`/#/merchant/products/${created.product_id}/edit`);
  await page.getByRole('button', { name: '取消' }).click();
  await expect(page).toHaveURL(/#\/merchant\/products$/);

  await page.goto(`/#/merchant/products/${created.product_id}/edit`);
  await page.getByLabel('商品名称').fill(name + ' 改');
  await page.getByRole('button', { name: '取消' }).click();
  const dialog = page.getByRole('dialog', { name: '放弃修改' });
  await expect(dialog).toBeVisible();
  await dialog.getByRole('button', { name: '取消' }).click();
  await expect(page.getByLabel('商品名称')).toHaveValue(name + ' 改');

  await page.getByRole('button', { name: '取消' }).first().click();
  await dialog.getByRole('button', { name: '放弃修改' }).click();
  await expect(page).toHaveURL(/#\/merchant\/products$/);
  const res = await api(request, session.token, 'GET', `/merchant/products/${created.product_id}`);
  expect((await res.json()).name).toBe(name);
});

test('服务端出错时显示错误且可以重试', async ({ page, request }, testInfo) => {
  const name = uniqueName(testInfo, '出错');
  const created = await (
    await api(request, session.token, 'POST', '/merchant/products', { name, category_id: 'c_mouse', price: '30' })
  ).json();
  await signedIn(page, session);
  let fail = true;
  await page.route((url) => url.pathname === `/api/v1/merchant/products/${created.product_id}`, async (route) => {
    if (route.request().method() === 'PATCH' && fail) {
      await route.fulfill({ status: 500, json: { code: 'internal_error', message: '服务暂时不可用', request_id: 'e2e' } });
      return;
    }
    await route.continue();
  });
  await page.goto(`/#/merchant/products/${created.product_id}/edit`);
  await page.getByLabel('推荐理由').fill('重试测试');
  await page.getByRole('button', { name: '保存修改' }).click();
  await expect(page.getByRole('alert').first()).toContainText('服务暂时不可用');
  await expect(page.getByRole('button', { name: '保存修改' })).toBeEnabled();
  fail = false;
  await page.getByRole('button', { name: '保存修改' }).click();
  await expect(page.locator('.flash')).toContainText(`已保存「${name}」`);
});

test('风控中的商品不能上下架', async ({ page }) => {
  await signedIn(page, session);
  await page.goto('/#/merchant/products/p_seed_powerbank/edit');
  await expect(page.getByLabel('状态')).toBeDisabled();
  await expect(page.getByText('风控审核中，不能自行上下架')).toBeVisible();
});
