import { useCallback, useEffect, useEffectEvent, useState } from 'react';
import { ApiError } from '../api/http';

export type RequestState<T> =
  | { status: 'loading' }
  | { status: 'error'; message: string }
  | { status: 'ok'; data: T };

type Settled<T> = { key: string; state: Exclude<RequestState<T>, { status: 'loading' }> };

// useRequest 在 key 变化或调用 reload 时重新加载。结果按请求 key 记录：key 变了而新结果还没回来时即为 loading，
// 过期请求的结果被丢弃，快速切换时不会显示旧数据。
export function useRequest<T>(key: string, load: () => Promise<T>): [RequestState<T>, () => void] {
  const [attempt, setAttempt] = useState(0);
  const [settled, setSettled] = useState<Settled<T> | null>(null);
  const requestKey = `${key}#${attempt}`;
  const run = useEffectEvent(load);

  useEffect(() => {
    let active = true;
    run()
      .then((data) => {
        if (active) setSettled({ key: requestKey, state: { status: 'ok', data } });
      })
      .catch((err: unknown) => {
        if (active) {
          const message = err instanceof ApiError ? err.message : '加载失败，请稍后重试';
          setSettled({ key: requestKey, state: { status: 'error', message } });
        }
      });
    return () => {
      active = false;
    };
  }, [requestKey]);

  const reload = useCallback(() => setAttempt((n) => n + 1), []);
  const state: RequestState<T> = settled?.key === requestKey ? settled.state : { status: 'loading' };
  return [state, reload];
}
