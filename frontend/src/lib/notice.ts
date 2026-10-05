import { useSyncExternalStore } from 'react';

// 全局通知：操作结果、登录失效等提示统一显示在页面顶部的通知区（App 里的 NoticeHost）。
// 通知跨页面保留，所以“保存后跳回列表”这类场景也能看到结果。成功和提示类 5 秒后自动消失，错误需要手动关闭。

export type NoticeKind = 'success' | 'info' | 'error';

export interface Notice {
  id: number;
  kind: NoticeKind;
  message: string;
}

export const AUTO_DISMISS_MS = 5000;
const MAX_NOTICES = 3;

let notices: Notice[] = [];
let nextID = 1;
const listeners = new Set<() => void>();
const timers = new Map<number, ReturnType<typeof setTimeout>>();

function emit() {
  for (const l of listeners) l();
}

export function notify(kind: NoticeKind, message: string): number {
  // 同样的提示已经在显示时不重复堆叠（例如多个请求同时遇到登录失效）。
  const same = notices.find((n) => n.kind === kind && n.message === message);
  if (same) return same.id;
  const id = nextID++;
  notices = [...notices, { id, kind, message }].slice(-MAX_NOTICES);
  if (kind !== 'error') {
    timers.set(
      id,
      setTimeout(() => dismiss(id), AUTO_DISMISS_MS),
    );
  }
  emit();
  return id;
}

export function dismiss(id: number): void {
  const timer = timers.get(id);
  if (timer) clearTimeout(timer);
  timers.delete(id);
  if (!notices.some((n) => n.id === id)) return;
  notices = notices.filter((n) => n.id !== id);
  emit();
}

/** 清空全部通知（测试用）。 */
export function resetNotices(): void {
  for (const timer of timers.values()) clearTimeout(timer);
  timers.clear();
  notices = [];
  emit();
}

function subscribe(onChange: () => void): () => void {
  listeners.add(onChange);
  return () => listeners.delete(onChange);
}

export function useNotices(): Notice[] {
  return useSyncExternalStore(subscribe, () => notices);
}
