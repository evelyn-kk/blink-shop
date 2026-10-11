import { defineConfig, devices } from '@playwright/test';

// Web 端到端测试：直接访问运行中的前端（默认 http://localhost:5173，其 /api 代理到真实后端）。
// 启动方式见 quality/README.md。
function extraBrowsers() {
  const wanted = (process.env.BLINK_E2E_BROWSERS ?? '').split(',').map((b) => b.trim()).filter(Boolean);
  const all = {
    firefox: { name: 'firefox', use: { ...devices['Desktop Firefox'], viewport: { width: 1280, height: 800 } } },
    webkit: { name: 'webkit', use: { ...devices['Desktop Safari'], viewport: { width: 1280, height: 800 } } },
  };
  return wanted.map((b) => all[b]).filter(Boolean);
}

export default defineConfig({
  testDir: './e2e',
  outputDir: './reports/e2e/artifacts',
  fullyParallel: false,
  // 串行执行：商家用例会临时创建商品，目录用例断言商品总数。
  workers: 1,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: [['list'], ['html', { outputFolder: './reports/e2e/html', open: 'never' }]],
  use: {
    baseURL: process.env.WEB_BASE_URL ?? 'http://localhost:5173',
    trace: 'retain-on-failure',
    locale: 'zh-CN',
    timezoneId: 'Asia/Shanghai',
  },
  projects: [
    { name: 'desktop', use: { ...devices['Desktop Chrome'], viewport: { width: 1280, height: 800 } } },
    // 验收时加跑其他主流桌面浏览器：BLINK_E2E_BROWSERS=firefox,webkit（CI 默认只跑 Chromium）
    ...extraBrowsers(),
    // 窄屏：iPhone SE 宽度，用 Chromium 渲染。
    { name: 'narrow', use: { ...devices['Desktop Chrome'], viewport: { width: 375, height: 740 }, isMobile: true, hasTouch: true } },
  ],
});
