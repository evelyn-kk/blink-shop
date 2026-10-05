import { expect, test } from '@playwright/test';

// 知识资料：商家工作台与平台管理，真实 API。资料没有删除接口，每个用例的内容都带唯一标记，重复运行不会撞上去重。

const PASSWORD = 'BlinkDev#2026';
const shots = (testInfo, name) => `reports/e2e/screens/${testInfo.project.name}-documents-${name}.png`;
const uniq = (testInfo) => `${testInfo.project.name}-${Date.now()}-${Math.floor(Math.random() * 1e6)}`;

async function apiLogin(request, username) {
  const res = await request.post('/api/v1/auth/login', { data: { username, password: PASSWORD } });
  expect(res.ok(), `login ${username}`).toBeTruthy();
  return res.json();
}

async function signedIn(page, session) {
  await page.addInitScript((s) => window.localStorage.setItem('blink_shop.session', JSON.stringify(s)), session);
}

test('商家添加常见问题：切成问答片段，详情可查看，列表可筛选', async ({ page, request }, testInfo) => {
  await signedIn(page, await apiLogin(request, 'blink_merchant'));
  const tag = uniq(testInfo);
  const title = `E2E 售后问答 ${tag}`;
  await page.goto('/#/merchant/documents');
  await expect(page.getByRole('heading', { name: '知识资料' })).toBeVisible();
  await page.screenshot({ path: shots(testInfo, 'list'), fullPage: true });

  await page.getByRole('link', { name: '添加资料' }).click();
  await expect(page.getByRole('heading', { name: '添加资料' })).toBeVisible();
  await page.getByLabel('标题').fill(title);
  await page.getByLabel('资料类型').selectOption('faq');
  await page.getByLabel('关联商品').selectOption({ label: 'Blink Vista Pro' });
  await page.getByLabel('内容').fill(`问：保修多久？\n答：整机一年保修（${tag}）。\n\n问：支持以旧换新吗？\n答：支持，到店评估后抵扣。`);
  await page.screenshot({ path: shots(testInfo, 'form'), fullPage: true });
  await page.getByRole('button', { name: '提交' }).click();

  await expect(page.locator('.flash').last()).toContainText(`「${title}」已入库，共 2 个片段`);
  await expect(page.getByRole('heading', { name: title })).toBeVisible();
  await expect(page.getByText('已入库').first()).toBeVisible();
  const chunks = page.locator('.chunk');
  await expect(chunks).toHaveCount(2);
  await expect(chunks.nth(0).locator('.chunk-title')).toHaveText('保修多久？');
  await expect(chunks.nth(1).locator('.chunk-title')).toHaveText('支持以旧换新吗？');
  await expect(page.getByText('p_seed_vista')).toBeVisible();
  await page.screenshot({ path: shots(testInfo, 'detail'), fullPage: true });

  await page.getByRole('link', { name: '← 返回资料列表' }).click();
  await page.getByLabel('按标题查找').fill(tag);
  await page.getByRole('button', { name: '查找' }).click();
  await expect(page.getByText('共 1 份')).toBeVisible();
  const row = page.getByRole('listitem').filter({ hasText: title });
  await expect(row).toContainText('常见问题');
  await expect(row).toContainText('2 个片段');
  await page.getByRole('link', { name: '入库失败' }).click();
  await expect(page.getByText('没有符合条件的资料')).toBeVisible();
});

test('采集网页源码：去掉脚本和导航；重复提交提示已有资料', async ({ page, request }, testInfo) => {
  await signedIn(page, await apiLogin(request, 'blink_merchant'));
  const tag = uniq(testInfo);
  await page.goto('/#/merchant/documents/new');
  await page.getByLabel('网页源码').check();
  const html = `<html><head><title>评测 ${tag}</title><script>steal()</script></head><body><nav>首页 | 分类</nav><h1>屏幕</h1><p>120Hz 高刷屏 ${tag}。</p></body></html>`;
  await page.getByLabel('内容').fill(html);
  await page.getByRole('button', { name: '提交' }).click();
  await expect(page.locator('.flash').last()).toContainText(`「评测 ${tag}」已入库，共 1 个片段`);
  await expect(page.locator('.chunk-content')).toHaveText(`屏幕\n\n120Hz 高刷屏 ${tag}。`);
  await expect(page.getByText('网页文章')).toBeVisible();

  await page.goto('/#/merchant/documents/new');
  await page.getByLabel('网页源码').check();
  await page.getByLabel('内容').fill(html.replace('<nav>首页 | 分类</nav>', '<nav>另一个导航</nav>'));
  await page.getByRole('button', { name: '提交' }).click();
  await expect(page.locator('.flash').last()).toContainText(`内容与已有资料「评测 ${tag}」相同，没有重复入库`);
});

test('表单校验：必填、地址格式、内网地址被拒并定位到字段；连点只提交一次', async ({ page, request }, testInfo) => {
  const session = await apiLogin(request, 'blink_merchant');
  await signedIn(page, session);
  await page.goto('/#/merchant/documents/new');
  await page.getByRole('button', { name: '提交' }).click();
  await expect(page.getByRole('alert')).toContainText('请先修正标出的字段');
  await expect(page.getByLabel('标题')).toHaveAttribute('aria-invalid', 'true');
  await expect(page.getByLabel('标题')).toBeFocused();
  await expect(page.getByText('请填写资料标题')).toBeVisible();

  await page.getByLabel('网页地址').check();
  await page.getByRole('textbox', { name: '网页地址' }).fill('ftp://example.com/a');
  await page.getByRole('button', { name: '提交' }).click();
  await expect(page.getByText('请填写以 http:// 或 https:// 开头的地址')).toBeVisible();

  // 服务端 SSRF 拦截：错误显示在地址输入框下并聚焦。
  await page.getByRole('textbox', { name: '网页地址' }).fill('http://169.254.169.254/latest/meta-data/');
  await page.getByRole('button', { name: '提交' }).click();
  await expect(page.locator('#f-body-error')).toHaveText('不能访问内网或保留地址');
  await expect(page.getByRole('textbox', { name: '网页地址' })).toBeFocused();
  await page.screenshot({ path: shots(testInfo, 'url-blocked'), fullPage: true });

  // 连点提交：请求未返回前再次点击不会再发请求。
  const tag = uniq(testInfo);
  let posts = 0;
  await page.route('**/api/v1/merchant/documents', async (route) => {
    if (route.request().method() === 'POST') {
      posts++;
      await new Promise((r) => setTimeout(r, 300));
    }
    await route.continue();
  });
  await page.getByLabel('文字资料').check();
  await page.getByLabel('标题').fill(`E2E 连点 ${tag}`);
  await page.getByLabel('内容').fill(`连点测试 ${tag}`);
  const submit = page.getByRole('button', { name: '提交' });
  await submit.click();
  await submit.click({ force: true }).catch(() => {});
  await expect(page.locator('.flash').last()).toContainText(`「E2E 连点 ${tag}」已入库`);
  expect(posts).toBe(1);
});

test('取消填写需要确认，放弃后回到列表', async ({ page, request }) => {
  await signedIn(page, await apiLogin(request, 'blink_merchant'));
  await page.goto('/#/merchant/documents/new');
  await page.getByLabel('内容').fill('写了一半');
  await page.getByRole('button', { name: '取消' }).click();
  const dialog = page.getByRole('dialog', { name: '放弃填写' });
  await dialog.getByRole('button', { name: '取消' }).click();
  await expect(page.getByLabel('内容')).toHaveValue('写了一半');
  await page.getByRole('button', { name: '取消' }).click();
  await dialog.getByRole('button', { name: '放弃' }).click();
  await expect(page).toHaveURL(/#\/merchant\/documents$/);
});

test('管理员采集平台资料，按归属筛选；商家不能进入平台管理', async ({ page, request }, testInfo) => {
  await page.goto('/#/login');
  await page.getByLabel('账号').fill('blink_admin');
  await page.getByLabel('密码').fill(PASSWORD);
  await page.getByRole('button', { name: '登录' }).click();
  await expect(page).toHaveURL(/#\/admin\/documents$/);
  await expect(page.getByRole('heading', { name: '知识资料（全部）' })).toBeVisible();

  const tag = uniq(testInfo);
  await page.getByRole('link', { name: '采集资料' }).click();
  await expect(page.getByLabel('归属')).toHaveValue('');
  await page.getByLabel('JSON').check();
  await page.getByLabel('标题').fill(`E2E 平台规则 ${tag}`);
  await page.getByLabel('内容').fill(JSON.stringify({ rule: `满 300 减 30（${tag}）`, scope: '全平台' }));
  await page.getByRole('button', { name: '提交' }).click();
  await expect(page.locator('.flash').last()).toContainText(`「E2E 平台规则 ${tag}」已入库`);
  await expect(page.getByText('平台资料')).toBeVisible();
  await expect(page.locator('.chunk-content')).toContainText('rule: 满 300 减 30');

  await page.getByRole('link', { name: '← 返回资料列表' }).click();
  await page.getByLabel('归属').selectOption('');
  await page.getByLabel('按标题查找').fill(tag);
  await page.getByRole('button', { name: '查找' }).click();
  await expect(page).toHaveURL(/merchant_id=/);
  // 等筛选后的列表渲染出来（筛选表单随之重建），再改筛选条件。
  await expect(page.getByText('共 1 份')).toBeVisible();
  await expect(page.getByRole('listitem').filter({ hasText: `E2E 平台规则 ${tag}` })).toContainText('平台');
  await page.getByLabel('归属').selectOption({ label: 'Blink 数码旗舰店' });
  await page.getByRole('button', { name: '查找' }).click();
  await expect(page.getByText('没有符合条件的资料')).toBeVisible();
  await page.screenshot({ path: shots(testInfo, 'admin'), fullPage: true });

  // 商家访问平台管理页面：前端提示无权访问，接口同样 403。
  const merchant = await apiLogin(request, 'blink_merchant');
  await page.evaluate((s) => window.localStorage.setItem('blink_shop.session', JSON.stringify(s)), merchant);
  await page.goto('/#/admin/documents');
  await page.reload();
  await expect(page.getByText('当前账号不是管理员，无权访问平台管理')).toBeVisible();
  const res = await request.get('/api/v1/admin/documents', { headers: { Authorization: `Bearer ${merchant.token}` } });
  expect(res.status()).toBe(403);
});

test('接口失败时显示错误并可以重试', async ({ page, request }) => {
  await signedIn(page, await apiLogin(request, 'blink_merchant'));
  let fail = true;
  await page.route('**/api/v1/merchant/documents?*', (route) =>
    fail ? route.fulfill({ status: 500, json: { code: 'internal_error', message: '服务暂时不可用' } }) : route.continue(),
  );
  await page.route('**/api/v1/merchant/documents', (route) =>
    fail ? route.fulfill({ status: 500, json: { code: 'internal_error', message: '服务暂时不可用' } }) : route.continue(),
  );
  await page.goto('/#/merchant/documents');
  await expect(page.getByRole('alert')).toContainText('服务暂时不可用');
  fail = false;
  await page.getByRole('button', { name: '重试' }).click();
  await expect(page.getByText(/共 \d+ 份/)).toBeVisible();
});
