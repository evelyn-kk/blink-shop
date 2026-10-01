import { expect, test } from '@playwright/test';

// 公开目录：用真实 API 走搜索 → 详情；窄屏下验证长标题、无图、空态不撑破页面。
// 依赖开发种子数据（go run ./cmd/seed）。

// 只匹配商品列表接口（/api/v1/products?...），不匹配详情等子路径。
const isProductList = (url) => url.pathname === '/api/v1/products';

const shots = (testInfo, name) => `reports/e2e/screens/${testInfo.project.name}-${name}.png`;

async function expectNoHorizontalScroll(page) {
  const overflow = await page.evaluate(() => document.documentElement.scrollWidth - document.documentElement.clientWidth);
  expect(overflow, '页面不应出现横向滚动').toBeLessThanOrEqual(0);
}

test('搜索 → 详情 → 返回列表保留条件', async ({ page }, testInfo) => {
  await page.goto('/#/products');
  await expect(page.getByText('共 6 件商品')).toBeVisible();
  // 下架、风控、已删除商品不出现。
  for (const hidden of ['Blink 便携音箱 S1', 'Blink 快充移动电源', 'Blink Nova 9（停产）']) {
    await expect(page.getByRole('heading', { name: hidden })).toHaveCount(0);
  }
  await page.screenshot({ path: shots(testInfo, 'list'), fullPage: true });
  await expectNoHorizontalScroll(page);

  await page.getByLabel('关键词').fill('降噪耳机');
  await page.getByRole('button', { name: '搜索' }).click();
  await expect(page).toHaveURL(/keyword=%E9%99%8D%E5%99%AA/);
  await expect(page.getByText('共 1 件商品')).toBeVisible();
  await page.getByRole('link', { name: /Blink Air 降噪耳机/ }).click();

  await expect(page.getByRole('heading', { level: 2, name: 'Blink Air 降噪耳机' })).toBeVisible();
  await expect(page.getByText('¥599.00').first()).toBeVisible();
  await expect(page.getByText('库存紧张').first()).toBeVisible();
  await expect(page.getByText('不支持游泳佩戴')).toBeVisible();
  await expect(page.getByText('9.5 折', { exact: true })).toBeVisible();
  await expect(page.getByText('满 300 减 30', { exact: true })).toBeVisible();
  await expect(page.getByRole('cell', { name: /Blink Air 白色/ })).toBeVisible();
  await expect(page.getByText('暂无评价')).toBeVisible();
  // 商品图来自后端内嵌资源，真实加载成功。
  const img = page.locator('img.gallery-main');
  await expect(img).toBeVisible();
  expect(await img.evaluate((el) => el.naturalWidth)).toBeGreaterThan(0);
  await page.screenshot({ path: shots(testInfo, 'detail'), fullPage: true });
  await expectNoHorizontalScroll(page);

  await page.getByRole('link', { name: '← 返回商品列表' }).click();
  await expect(page.getByLabel('关键词')).toHaveValue('降噪耳机');
  await expect(page.getByText('共 1 件商品')).toBeVisible();
});

test('分类筛选与分页', async ({ page }) => {
  await page.goto('/#/products');
  await page.getByLabel('分类').selectOption('c_office');
  await page.getByRole('button', { name: '搜索' }).click();
  await expect(page.getByText('共 2 件商品')).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Blink 静音无线鼠标 M2' })).toBeVisible();
  await expect(page.getByRole('heading', { name: 'Blink 机械键盘 K8' })).toBeVisible();
  // 无货商品照常展示并标明状态。
  await expect(page.getByText('暂时无货')).toBeVisible();
  // 6 件商品一页 12 件，不出现分页。
  await page.goto('/#/products');
  await expect(page.getByRole('navigation', { name: '分页' })).toHaveCount(0);
});

test('评价与商家回复', async ({ page }) => {
  await page.goto('/#/products/p_seed_mouse');
  await expect(page.getByRole('heading', { name: '评价（1）' })).toBeVisible();
  await expect(page.getByText('演***')).toBeVisible();
  await expect(page.getByText('商家回复：感谢支持！')).toBeVisible();
  await expect(page.getByLabel('5 星')).toBeVisible();
});

test('空态与不可见商品', async ({ page }, testInfo) => {
  await page.goto('/#/products?keyword=' + encodeURIComponent('完全不存在的商品'));
  await expect(page.getByText('没有找到符合条件的商品')).toBeVisible();
  await page.screenshot({ path: shots(testInfo, 'empty'), fullPage: true });
  await page.getByRole('link', { name: '清除筛选' }).click();
  await expect(page.getByText('共 6 件商品')).toBeVisible();

  for (const id of ['p_seed_speaker', 'p_seed_powerbank', 'p_seed_legacy', 'nope']) {
    await page.goto(`/#/products/${id}`);
    await expect(page.getByText('商品不存在，或已下架')).toBeVisible();
  }
});

test('长标题、无图、图片加载失败不撑破布局', async ({ page }, testInfo) => {
  const longName = 'Blink 超长标题测试商品'.repeat(6) + 'NoSpaceVeryLongModelNameWithoutAnyBreakOpportunity1234567890';
  // 在真实响应上改出边界数据：第一项超长标题且无图，第二项图片地址失效。
  await page.route(isProductList, async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    body.items[0] = { ...body.items[0], name: longName, image_url: '' };
    body.items[1] = { ...body.items[1], image_url: '/api/v1/assets/catalog/products/missing.png' };
    await route.fulfill({ response: res, json: body });
  });
  await page.route((url) => url.pathname === '/api/v1/products/p_seed_nova', async (route) => {
    const res = await route.fetch();
    const body = await res.json();
    await route.fulfill({ response: res, json: { ...body, name: longName, image_url: '', image_urls: [] } });
  });

  await page.goto('/#/products');
  await expect(page.getByText('共 6 件商品')).toBeVisible();
  await expect(page.getByRole('img', { name: /暂无图片/ })).toHaveCount(2);
  const title = page.getByRole('heading', { name: longName });
  await expect(title).toBeVisible();
  // 卡片标题最多两行。
  const lines = await title.evaluate((el) => Math.round(el.getBoundingClientRect().height / parseFloat(getComputedStyle(el).lineHeight)));
  expect(lines).toBeLessThanOrEqual(2);
  await expectNoHorizontalScroll(page);
  await page.screenshot({ path: shots(testInfo, 'list-edge'), fullPage: true });

  await page.goto('/#/products/p_seed_nova');
  await expect(page.getByRole('heading', { level: 2, name: longName })).toBeVisible();
  await expect(page.getByRole('img', { name: /暂无图片/ })).toBeVisible();
  await expectNoHorizontalScroll(page);
  await page.screenshot({ path: shots(testInfo, 'detail-edge'), fullPage: true });
});

test('接口失败时显示错误并可重试', async ({ page }) => {
  let fail = true;
  await page.route(isProductList, async (route) => {
    if (fail) {
      await route.fulfill({ status: 500, json: { code: 'internal_error', message: '服务暂时不可用', request_id: 'e2e' } });
      return;
    }
    await route.continue();
  });
  await page.goto('/#/products');
  await expect(page.getByRole('alert')).toContainText('服务暂时不可用');
  fail = false;
  await page.getByRole('button', { name: '重试' }).click();
  await expect(page.getByText('共 6 件商品')).toBeVisible();
});

test('金额不在数字中间断行', async ({ page }) => {
  await page.goto('/#/products/p_seed_nova');
  const cell = page.getByRole('cell', { name: '¥2,999.00' });
  await expect(cell).toBeVisible();
  // 金额文字只占一个行框（断行时会变成两个）。
  const text = await cell.evaluate((el) => {
    const r = document.createRange();
    r.selectNodeContents(el);
    return r.getClientRects().length;
  });
  expect(text, '金额应在一行内').toBe(1);
});
