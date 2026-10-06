import type {
  AdminAccount,
  AdminMerchant,
  AdminProduct,
  AdminReview,
  AuditLog,
  ConfigItem,
  MerchantPromotion,
  Page,
  RiskOverview,
} from '../types/api';
import { request } from './http';

export type AdminResource = 'accounts' | 'merchants' | 'products' | 'promotions' | 'reviews';

export interface AdminRow {
  accounts: AdminAccount;
  merchants: AdminMerchant;
  products: AdminProduct;
  promotions: MerchantPromotion;
  reviews: AdminReview;
}

function query(params: Record<string, string | number | undefined>): string {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) {
    if (value !== undefined && value !== '' && !(key === 'page' && value === 1)) search.set(key, String(value));
  }
  const text = search.toString();
  return text ? `?${text}` : '';
}

export function listAdmin<R extends AdminResource>(resource: R, params: Record<string, string | number | undefined>): Promise<Page<AdminRow[R]>> {
  return request(`/admin/${resource}${query(params)}`);
}

/** 修改状态；改为非正常状态时 reason 必填（记入操作审计）。 */
export function changeStatus<R extends AdminResource>(resource: R, id: string, status: string, reason: string): Promise<AdminRow[R]> {
  return request(`/admin/${resource}/${encodeURIComponent(id)}`, { method: 'PATCH', body: JSON.stringify({ status, reason }) });
}

export function listConfigs(): Promise<{ items: ConfigItem[] }> {
  return request('/admin/configs');
}

/** 空值表示恢复默认值。 */
export function updateConfig(key: string, value: string): Promise<ConfigItem> {
  return request(`/admin/configs/${encodeURIComponent(key)}`, { method: 'PATCH', body: JSON.stringify({ value }) });
}

export function getRiskOverview(): Promise<RiskOverview> {
  return request('/admin/risk/overview');
}

export function listAuditLogs(params: { targetType?: string; targetID?: string; operatorID?: string; page?: number; pageSize?: number }): Promise<Page<AuditLog>> {
  return request(
    `/admin/audit-logs${query({ target_type: params.targetType, target_id: params.targetID, operator_id: params.operatorID, page: params.page, page_size: params.pageSize })}`,
  );
}
