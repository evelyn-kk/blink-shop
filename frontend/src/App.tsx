import { useEffect, useState } from 'react';
import { getHealth } from './api/health';
import { ApiError } from './api/http';

type ApiState = { kind: 'loading' } | { kind: 'ok' } | { kind: 'error'; message: string };

export default function App() {
  const [api, setApi] = useState<ApiState>({ kind: 'loading' });

  useEffect(() => {
    getHealth()
      .then(() => setApi({ kind: 'ok' }))
      .catch((err: unknown) =>
        setApi({ kind: 'error', message: err instanceof ApiError ? err.message : '未知错误' }),
      );
  }, []);

  return (
    <main style={{ fontFamily: 'system-ui, sans-serif', padding: 24 }}>
      <h1>Blink Shop 管理端</h1>
      <p>
        后端状态：
        {api.kind === 'loading' && '检测中…'}
        {api.kind === 'ok' && '正常'}
        {api.kind === 'error' && `不可用（${api.message}）`}
      </p>
    </main>
  );
}
