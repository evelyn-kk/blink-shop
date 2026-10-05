import { readdirSync, readFileSync } from 'node:fs';
import path from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { getSession } from '../lib/session';
import { useNotices } from '../lib/notice';
import { renderHook } from '@testing-library/react';
import { signIn } from '../test/session';
import { ApiError, request, requestBlob } from './http';

// API client 契约测试：用后端的 HTTP fixture（backend/fixtures/http，后端 TestHTTPFixtures 用真实服务验证过）
// 模拟响应，确认客户端对每种错误都给出可展示的 ApiError（状态码、机器码、中文说明、出错字段、请求 ID）。

interface Fixture {
  description?: string;
  request: { method: string; path: string; body?: unknown };
  response: { status: number; body: Record<string, unknown> };
}

// vitest 在 frontend 目录下运行。
const fixtureDir = path.resolve(process.cwd(), '../backend/fixtures/http');
const fixtures = readdirSync(fixtureDir)
  .filter((f) => f.endsWith('.json'))
  .map((f) => ({ name: f.replace(/\.json$/, ''), fx: JSON.parse(readFileSync(path.join(fixtureDir, f), 'utf8')) as Fixture }));

function respond(status: number, body: unknown, headers: Record<string, string> = { 'Content-Type': 'application/json' }) {
  return vi.fn(async () => new Response(status === 204 ? null : typeof body === 'string' ? body : JSON.stringify(body), { status, headers }));
}

// fixture 里随机值写成占位符，这里换成固定值。
function concrete(body: Record<string, unknown>): Record<string, unknown> {
  return JSON.parse(JSON.stringify(body).replace(/"<request_id>"/g, '"req_fixture"'));
}

afterEach(() => vi.unstubAllGlobals());

describe('错误响应契约', () => {
  const errors = fixtures.filter(({ fx }) => fx.response.status >= 400);
  it('fixture 至少覆盖 400/401/403/404/409/413/429/503', () => {
    const statuses = new Set(errors.map(({ fx }) => fx.response.status));
    for (const s of [400, 401, 403, 404, 409, 413, 429, 503]) expect(statuses, `status ${s}`).toContain(s);
  });
  it.each(errors)('$name', async ({ fx }) => {
    const body = concrete(fx.response.body);
    vi.stubGlobal('fetch', respond(fx.response.status, body));
    const err = await request(fx.request.path.replace(/^\/api\/v1/, ''), { method: fx.request.method }).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    const apiErr = err as ApiError;
    expect(apiErr.status).toBe(fx.response.status);
    expect(apiErr.code).toBe(body.code);
    expect(apiErr.message).toBe(body.message);
    expect(apiErr.message).toMatch(/\p{Script=Han}/u); // 可以直接展示的中文
    expect(apiErr.field).toBe(body.field);
    expect(apiErr.requestId).toBe('req_fixture');
  });
});

describe('成功响应契约', () => {
  it.each(fixtures.filter(({ fx }) => fx.response.status < 300))('$name 原样返回响应体', async ({ fx }) => {
    const body = concrete(fx.response.body);
    vi.stubGlobal('fetch', respond(fx.response.status, body));
    await expect(request(fx.request.path.replace(/^\/api\/v1/, ''))).resolves.toEqual(body);
  });
});

describe('请求与会话', () => {
  it('带上 token 和 JSON 头，路径加 /api/v1 前缀，token 不进 URL', async () => {
    signIn('merchant', 3600_000, 'tok-abc');
    const fetch = respond(200, { ok: true });
    vi.stubGlobal('fetch', fetch);
    await request('/merchant/orders?page=2', { method: 'PATCH', body: JSON.stringify({ status: 'shipped' }) });
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/v1/merchant/orders?page=2');
    expect(url).not.toContain('tok-abc');
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer tok-abc');
    expect(headers.get('Content-Type')).toBe('application/json');
  });

  it('上传 FormData 时不覆盖 Content-Type（由浏览器带 boundary）', async () => {
    const fetch = respond(201, { file: {} });
    vi.stubGlobal('fetch', fetch);
    await request('/files', { method: 'POST', body: new FormData() });
    const [, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(new Headers(init.headers).has('Content-Type')).toBe(false);
  });

  it('204 返回 undefined', async () => {
    vi.stubGlobal('fetch', respond(204, null));
    await expect(request('/x')).resolves.toBeUndefined();
  });

  it('网络断开给出可读的错误', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new TypeError('Failed to fetch'))));
    await expect(request('/products')).rejects.toMatchObject({ status: 0, code: 'network_error', message: '网络连接失败，请稍后重试' });
  });

  it('取消请求时原样抛出 AbortError，不当成网络错误', async () => {
    vi.stubGlobal('fetch', vi.fn(async () => Promise.reject(new DOMException('aborted', 'AbortError'))));
    await expect(request('/products')).rejects.toMatchObject({ name: 'AbortError' });
  });

  it('错误响应不是约定的 JSON 时给出带状态码的说明', async () => {
    vi.stubGlobal('fetch', respond(502, '<html>Bad Gateway</html>', { 'Content-Type': 'text/html' }));
    await expect(request('/products')).rejects.toMatchObject({ status: 502, code: 'unknown_error', message: '请求失败（502）' });
  });

  it('带 token 的请求 401：清除会话并提示重新登录', async () => {
    signIn('admin');
    vi.stubGlobal('fetch', respond(401, { code: 'unauthorized', message: '登录已失效，请重新登录' }));
    await expect(request('/admin/orders')).rejects.toMatchObject({ status: 401 });
    expect(getSession()).toBeNull();
    const { result } = renderHook(() => useNotices());
    expect(result.current.map((n) => n.message)).toEqual(['登录已失效，请重新登录']);
  });

  it('文件下载同样处理 401', async () => {
    signIn('user');
    vi.stubGlobal('fetch', respond(401, { code: 'unauthorized', message: '登录已失效，请重新登录' }));
    await expect(requestBlob('/files/file_x')).rejects.toMatchObject({ status: 401 });
    expect(getSession()).toBeNull();
  });

  it('未带 token 的 401（例如登录密码错误）不影响会话和通知', async () => {
    vi.stubGlobal('fetch', respond(401, { code: 'invalid_credential', message: '账号或密码错误' }));
    await expect(request('/auth/login', { method: 'POST', body: '{}' })).rejects.toMatchObject({ code: 'invalid_credential' });
    const { result } = renderHook(() => useNotices());
    expect(result.current).toEqual([]);
  });

  it('退出登录时 token 已失效不提示“登录已失效”', async () => {
    signIn('merchant');
    vi.stubGlobal('fetch', respond(401, { code: 'unauthorized', message: '登录已失效，请重新登录' }));
    await expect(request('/auth/logout', { method: 'POST' })).rejects.toMatchObject({ status: 401 });
    const { result } = renderHook(() => useNotices());
    expect(result.current).toEqual([]);
  });
});
