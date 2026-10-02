import type { ApiErrorBody } from '../types/api';
import { clearSession, getSession } from '../lib/session';

const API_BASE = '/api/v1';

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly requestId?: string;
  readonly field?: string;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message);
    this.name = 'ApiError';
    this.status = status;
    this.code = body.code;
    this.field = body.field;
    this.requestId = body.request_id;
  }
}

// 所有接口请求都经过这里，统一处理前缀、JSON、登录 token 和错误结构。
// 带 token 的请求收到 401 时清除本地会话（页面随之回到登录）。
export async function request<T>(path: string, init: RequestInit = {}): Promise<T> {
  const headers = new Headers(init.headers);
  const session = getSession();
  if (session && !headers.has('Authorization')) {
    headers.set('Authorization', `Bearer ${session.token}`);
  }
  if (init.body !== undefined && !(init.body instanceof FormData)) {
    headers.set('Content-Type', 'application/json');
  }

  let res: Response;
  try {
    res = await fetch(`${API_BASE}${path}`, { ...init, headers });
  } catch {
    throw new ApiError(0, { code: 'network_error', message: '网络连接失败，请稍后重试' });
  }

  if (!res.ok) {
    const body = await res.json().catch(() => null);
    if (res.status === 401 && headers.has('Authorization')) {
      clearSession();
    }
    throw new ApiError(
      res.status,
      isApiErrorBody(body) ? body : { code: 'unknown_error', message: `请求失败（${res.status}）` },
    );
  }
  if (res.status === 204) {
    return undefined as T;
  }
  return (await res.json()) as T;
}

function isApiErrorBody(value: unknown): value is ApiErrorBody {
  return (
    typeof value === 'object' &&
    value !== null &&
    typeof (value as ApiErrorBody).code === 'string' &&
    typeof (value as ApiErrorBody).message === 'string'
  );
}
