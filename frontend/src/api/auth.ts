import type { Session } from '../types/api';
import { request } from './http';

export function login(username: string, password: string): Promise<Session> {
  return request<Session>('/auth/login', { method: 'POST', body: JSON.stringify({ username, password }) });
}

export function logout(): Promise<{ ok: boolean }> {
  return request('/auth/logout', { method: 'POST' });
}
