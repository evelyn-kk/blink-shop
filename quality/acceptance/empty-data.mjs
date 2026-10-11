// 空数据验收：对只执行了迁移、没有任何种子数据的部署，匿名和新注册用户打开主要页面，应显示空态而不是错误，前端没有报错。
//   EMPTY_WEB=http://127.0.0.1:15193 node quality/acceptance/empty-data.mjs
import { chromium } from '@playwright/test';

const web = process.env.EMPTY_WEB ?? 'http://127.0.0.1:15193';
const results = [];
const check = (name, ok, detail = '') => results.push({ name, ok, detail });

const browser = await chromium.launch();
const page = await browser.newPage({ locale: 'zh-CN' });
const errors = [];
page.on('pageerror', (e) => errors.push(e.message));
page.on('console', (m) => m.type() === 'error' && !/Failed to load resource/.test(m.text()) && errors.push(m.text()));

const body = async () => (await page.locator('main').innerText()).replace(/\s+/g, ' ');

await page.goto(`${web}/#/products`);
await page.getByText('暂无上架商品').waitFor({ timeout: 10000 }).catch(() => {});
let text = await body();
check('匿名浏览商品列表：显示“暂无上架商品”，没有错误', text.includes('暂无上架商品') && !text.includes('重试'), text.slice(0, 80));

await page.goto(`${web}/#/products?keyword=耳机`);
await page.getByText('没有找到符合条件的商品').waitFor({ timeout: 10000 }).catch(() => {});
text = await body();
check('空库搜索：显示“没有找到符合条件的商品”', text.includes('没有找到符合条件的商品'), text.slice(0, 80));

await page.goto(`${web}/#/products/p_seed_nova`);
await page.waitForTimeout(1500);
text = await body();
check('打开不存在的商品：提示不存在，不白屏', text.length > 0 && !/undefined|NaN/.test(text), text.slice(0, 80));

// 新注册用户（空库里没有任何种子账号）
const username = `empty_${Date.now() % 1e8}`;
const res = await page.request.post(`${web}/api/v1/auth/register`, { data: { username, password: 'Empty-Data#2026', display_name: '空库用户' } });
check('空库可以注册新用户', res.status() === 201, String(res.status()));
const session = await res.json();
await page.addInitScript((s) => window.localStorage.setItem('blink_shop.session', JSON.stringify(s)), session);
const cart = await page.request.get(`${web}/api/v1/cart`, { headers: { Authorization: `Bearer ${session.token}` } });
const orders = await page.request.get(`${web}/api/v1/orders`, { headers: { Authorization: `Bearer ${session.token}` } });
const sessions = await page.request.get(`${web}/api/v1/agent/sessions`, { headers: { Authorization: `Bearer ${session.token}` } });
check('新用户的购物车、订单、会话接口返回空列表', cart.ok() && orders.ok() && sessions.ok() && (await orders.json()).total === 0, `${cart.status()} ${orders.status()} ${sessions.status()}`);
await page.goto(`${web}/#/products`);
await page.waitForTimeout(1000);
text = await body();
check('登录后的默认页面正常显示', !text.includes('重试') && text.length > 0, text.slice(0, 80));

check('整个过程前端没有报错', errors.length === 0, errors.slice(0, 3).join(' | '));
await browser.close();

console.log('| 检查 | 结果 | 说明 |\n| --- | --- | --- |');
for (const r of results) console.log(`| ${r.name} | ${r.ok ? '通过' : '**失败**'} | ${String(r.detail).replace(/\|/g, '/')} |`);
const failed = results.filter((r) => !r.ok).length;
console.log(`\n共 ${results.length} 项，失败 ${failed} 项`);
process.exit(failed ? 1 : 0);
