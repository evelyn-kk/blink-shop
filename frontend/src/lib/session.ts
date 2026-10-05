import { useSyncExternalStore } from 'react';
import type { Account, Session } from '../types/api';
import { notify } from './notice';

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

// setTimeout 的最大延迟约 24.8 天；更远的到期时间先等到上限再重新计算。
const MAX_TIMER_MS = 2 ** 31 - 1;
let expiryTimer: ReturnType<typeof setTimeout> | undefined;

// scheduleExpiry 在 token 到期时清除会话并提示重新登录：页面一直开着也不会带着过期 token 继续操作。
function scheduleExpiry(s: Session | null) {
  clearTimeout(expiryTimer);
  expiryTimer = undefined;
  if (!s) return;
  const left = Date.parse(s.expires_at) - Date.now();
  expiryTimer = setTimeout(
    () => {
      if (getSession()?.token !== s.token) return;
      if (Date.parse(s.expires_at) > Date.now()) {
        scheduleExpiry(s);
        return;
      }
      expireSession();
    },
    Math.min(Math.max(left, 0), MAX_TIMER_MS),
  );
}

function snapshot(): Session | null {
  const raw = readRaw();
  if (raw !== cachedRaw) {
    cachedRaw = raw;
    cached = read();
    scheduleExpiry(cached);
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

/** 登录已失效（token 过期、被撤销或账号被注销）：清除会话并提示。需要登录的页面随之跳到登录页，登录后回到原地址。 */
export function expireSession(): void {
  if (!getSession()) return;
  clearSession();
  notify('info', '登录已失效，请重新登录');
}

export function useSession(): Session | null {
  return useSyncExternalStore(subscribe, snapshot);
}

export function updateSessionAccount(account: Account): void {
  const s = getSession();
  if (s) saveSession({ ...s, account });
}
