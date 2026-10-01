// 与 backend/openapi.yaml 中 components.schemas 保持一致。

export interface ApiErrorBody {
  code: string;
  message: string;
  request_id?: string;
}

export interface Page<T> {
  page: number;
  page_size: number;
  total: number;
  items: T[];
}

export interface HealthResponse {
  status: 'ok';
}
