import { defineConfig, devices } from '@playwright/test';

// Web 端到端测试：直接访问运行中的前端（默认 http://localhost:5173，其 /api 代理到真实后端）。
// 启动方式见 quality/README.md。
export default defineConfig({
  testDir: './e2e',
  outputDir: './reports/e2e/artifacts',
  fullyParallel: false,
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
    // 窄屏：iPhone SE 宽度，用 Chromium 渲染。
    { name: 'narrow', use: { ...devices['Desktop Chrome'], viewport: { width: 375, height: 740 }, isMobile: true, hasTouch: true } },
  ],
});
