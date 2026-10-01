import type { HealthResponse } from '../types/api';
import { request } from './http';

export function getHealth(): Promise<HealthResponse> {
  return request<HealthResponse>('/health');
}
