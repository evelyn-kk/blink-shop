import { useSyncExternalStore } from 'react';
import type { Account, Session } from '../types/api';

// 登录会话保存在 localStorage（与上游一致）；token 只放在请求头里，不进 URL。
const KEY = 'blink_shop.session';
const CHANGED = 'blink-shop-session';

function read(): Session | null {
  try {
    const raw = window.localStorage.getItem(KEY);
    if (!raw) return null;
    const s = JSON.parse(raw) as Session;
    if (!s.token || !s.account || Date.parse(s.expires_at) <= Date.now()) {
      window.localStorage.removeItem(KEY);
      return null;
    }
    return s;
  } catch {
    return null;
  }
}

let cachedRaw: string | null = null;
let cached: Session | null = null;

function readRaw(): string | null {
  try {
    return window.localStorage.getItem(KEY);
  } catch {
    return null;
  }
}

function snapshot(): Session | null {
  const raw = readRaw();
  if (raw !== cachedRaw) {
    cachedRaw = raw;
    cached = read();
  }
  return cached;
}

function subscribe(onChange: () => void): () => void {
  window.addEventListener(CHANGED, onChange);
  window.addEventListener('storage', onChange); // 其他标签页登录/退出
  return () => {
    window.removeEventListener(CHANGED, onChange);
    window.removeEventListener('storage', onChange);
  };
}

export function getSession(): Session | null {
  return snapshot();
}

export function saveSession(s: Session): void {
  window.localStorage.setItem(KEY, JSON.stringify(s));
  window.dispatchEvent(new Event(CHANGED));
}

export function clearSession(): void {
  try {
    window.localStorage.removeItem(KEY);
  } catch {
    // 存储不可用时无需处理
  }
  window.dispatchEvent(new Event(CHANGED));
}

export function useSession(): Session | null {
  return useSyncExternalStore(subscribe, snapshot);
}

export function updateSessionAccount(account: Account): void {
  const s = getSession();
  if (s) saveSession({ ...s, account });
}
