import { render, screen, waitFor } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { getSession } from '../lib/session';
import { signIn } from '../test/session';
import type { Session } from '../types/api';
import { LoginPage } from './LoginPage';

function session(role: Session['account']['role']): Session {
  return {
    token: 'tok',
    token_type: 'Bearer',
    expires_at: new Date(Date.now() + 3600_000).toISOString(),
    account: {
      account_id: 'acct_1', username: 'u', display_name: '演示', avatar_url: '', phone: '', email: '', role,
      status: 'active', created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z',
    },
  };
}

function mockFetch(...responses: [number, unknown][]) {
  const fetch = vi.fn(async () => {
    const [status, body] = responses.shift() ?? [500, {}];
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

afterEach(() => vi.unstubAllGlobals());

describe('登录页', () => {
  it('必填校验：错误定位到字段并聚焦第一个，不发请求', async () => {
    const fetch = mockFetch();
    render(<LoginPage next="" />);
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    const username = screen.getByLabelText(/账号/);
    expect(username.getAttribute('aria-invalid')).toBe('true');
    expect(screen.getByText('请输入账号').id).toBe(username.getAttribute('aria-describedby'));
    expect(screen.getByText('请输入密码')).toBeTruthy();
    expect(document.activeElement).toBe(username);
    await userEvent.type(username, 'blink_admin');
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    expect(document.activeElement).toBe(screen.getByLabelText(/密码/));
    expect(screen.queryByText('请输入账号')).toBeNull();
    expect(fetch).not.toHaveBeenCalled();
  });

  it('只用键盘：Tab 到字段，回车提交，按角色进入默认页面', async () => {
    mockFetch([200, session('merchant')]);
    render(<LoginPage next="" />);
    await userEvent.tab();
    expect(document.activeElement).toBe(screen.getByLabelText(/账号/));
    await userEvent.keyboard('blink_merchant');
    await userEvent.tab();
    await userEvent.keyboard('secret{Enter}');
    await waitFor(() => expect(window.location.hash).toBe('#/merchant/products'));
    expect(getSession()?.account.role).toBe('merchant');
  });

  it('登录后回到原地址；管理员默认进资料页', async () => {
    const fetch = mockFetch([200, session('admin')]);
    render(<LoginPage next="#/admin/orders?status=paid" />);
    await userEvent.type(screen.getByLabelText(/账号/), '  blink_admin  ');
    await userEvent.type(screen.getByLabelText(/密码/), 'pw');
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    await waitFor(() => expect(window.location.hash).toBe('#/admin/orders?status=paid'));
    const [, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(JSON.parse(init.body as string)).toEqual({ username: 'blink_admin', password: 'pw' });
  });

  it.each([
    [401, { code: 'invalid_credential', message: '账号或密码错误' }, '账号或密码错误'],
    [403, { code: 'account_inactive', message: '账号已停用，无法登录' }, '账号已停用，无法登录'],
    [429, { code: 'rate_limited', message: '登录尝试过于频繁，请稍后再试' }, '登录尝试过于频繁，请稍后再试'],
  ])('服务端拒绝（%i）时原样提示，可以改了再试', async (status, body, text) => {
    mockFetch([status, body]);
    render(<LoginPage next="" />);
    await userEvent.type(screen.getByLabelText(/账号/), 'u');
    await userEvent.type(screen.getByLabelText(/密码/), 'bad');
    await userEvent.click(screen.getByRole('button', { name: '登录' }));
    expect((await screen.findByRole('alert')).textContent).toBe(text);
    expect(getSession()).toBeNull();
    expect(document.activeElement).toBe(screen.getByLabelText(/密码/));
    expect((screen.getByRole('button', { name: '登录' }) as HTMLButtonElement).disabled).toBe(false);
  });

  it('连点只提交一次，处理中按钮禁用', async () => {
    let resolve: (r: Response) => void = () => {};
    const fetch = vi.fn(() => new Promise<Response>((r) => (resolve = r)));
    vi.stubGlobal('fetch', fetch);
    render(<LoginPage next="" />);
    await userEvent.type(screen.getByLabelText(/账号/), 'u');
    await userEvent.type(screen.getByLabelText(/密码/), 'p');
    const button = screen.getByRole('button', { name: '登录' });
    await userEvent.click(button);
    await userEvent.click(button);
    expect(fetch).toHaveBeenCalledOnce();
    expect(screen.getByRole('button', { name: '登录中…' })).toHaveProperty('disabled', true);
    resolve(new Response(JSON.stringify(session('user')), { status: 200 }));
    await waitFor(() => expect(window.location.hash).toBe('#/products'));
  });

  it('已经登录时直接进入默认页面', async () => {
    signIn('admin');
    render(<LoginPage next="" />);
    await waitFor(() => expect(window.location.hash).toBe('#/admin/documents'));
  });
});
