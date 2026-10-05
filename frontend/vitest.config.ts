import { defineConfig, mergeConfig } from 'vitest/config';
import viteConfig from './vite.config';

// 组件与 API client 单测：jsdom 环境，不连真实后端（契约用 backend/fixtures/http 的样例）。
export default mergeConfig(
  viteConfig,
  defineConfig({
    test: {
      environment: 'jsdom',
      include: ['src/**/*.test.{ts,tsx}'],
      setupFiles: ['src/test/setup.ts'],
      restoreMocks: true,
    },
  }),
);
