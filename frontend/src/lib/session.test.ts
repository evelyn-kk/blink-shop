import { act, renderHook } from '@testing-library/react';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { signIn } from '../test/session';
import { useNotices } from './notice';
import { clearSession, getSession, useSession } from './session';

afterEach(() => {
  clearSession();
  vi.useRealTimers();
});

describe('会话', () => {
  it('刷新页面后从 localStorage 恢复会话', () => {
    const s = signIn('merchant');
    expect(getSession()).toEqual(s);
    expect(JSON.parse(window.localStorage.getItem('blink_shop.session') ?? 'null')).toEqual(s);
  });

  it('已过期的会话不会被读出，并从存储中删除', () => {
    signIn('merchant', -1000);
    expect(getSession()).toBeNull();
    expect(window.localStorage.getItem('blink_shop.session')).toBeNull();
  });

  it('损坏的存储内容按未登录处理', () => {
    window.localStorage.setItem('blink_shop.session', '{not json');
    expect(getSession()).toBeNull();
    window.localStorage.setItem('blink_shop.session', JSON.stringify({ token: '', account: null }));
    expect(getSession()).toBeNull();
  });

  it('页面开着时 token 到期：自动清除会话并提示重新登录', () => {
    vi.useFakeTimers();
    signIn('admin', 60_000);
    const { result } = renderHook(() => ({ session: useSession(), notices: useNotices() }));
    expect(result.current.session?.account.role).toBe('admin');
    act(() => vi.advanceTimersByTime(59_000));
    expect(result.current.session).not.toBeNull();
    act(() => vi.advanceTimersByTime(1_000));
    expect(result.current.session).toBeNull();
    expect(result.current.notices.map((n) => n.message)).toEqual(['登录已失效，请重新登录']);
  });

  it('主动退出不提示登录失效，也不会再触发到期提示', () => {
    vi.useFakeTimers();
    signIn('admin', 60_000);
    const { result } = renderHook(() => ({ session: useSession(), notices: useNotices() }));
    act(() => clearSession());
    act(() => vi.advanceTimersByTime(120_000));
    expect(result.current.session).toBeNull();
    expect(result.current.notices).toEqual([]);
  });

  it('其他标签页登录或退出时同步', () => {
    const { result } = renderHook(() => useSession());
    expect(result.current).toBeNull();
    let token = '';
    act(() => {
      token = signIn('merchant').token;
    });
    expect(result.current?.token).toBe(token);
    act(() => {
      window.localStorage.removeItem('blink_shop.session');
      window.dispatchEvent(new StorageEvent('storage', { key: 'blink_shop.session' }));
    });
    expect(result.current).toBeNull();
  });
});
