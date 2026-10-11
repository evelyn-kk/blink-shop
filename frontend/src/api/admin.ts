import type {
  AdminAccount,
  AdminRun,
  AdminRunDetail,
  EvalOverview,
  EvalReport,
  EvalToolResult,
  RunStatus,
  VectorStatus,
  AdminMerchant,
  AdminProduct,
  AdminReview,
  AuditLog,
  ConfigBackend,
  ConfigItem,
  MerchantPromotion,
  Page,
  PromptDetail,
  PromptPublishRecord,
  PromptSummary,
  PromptVersion,
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

export function listConfigs(): Promise<{ items: ConfigItem[]; backend: ConfigBackend }> {
  return request('/admin/configs');
}

/** 空值表示恢复默认值。 */
export function updateConfig(key: string, value: string): Promise<ConfigItem> {
  return request(`/admin/configs/${encodeURIComponent(key)}`, { method: 'PATCH', body: JSON.stringify({ value }) });
}

export function listPrompts(): Promise<{ items: PromptSummary[] }> {
  return request('/admin/prompts');
}

export function getPrompt(key: string): Promise<PromptDetail> {
  return request(`/admin/prompts/${encodeURIComponent(key)}`);
}

export function savePromptDraft(key: string, content: string): Promise<PromptVersion> {
  return request(`/admin/prompts/${encodeURIComponent(key)}`, { method: 'PATCH', body: JSON.stringify({ content }) });
}

/** 发布草稿（action=publish）或回滚到旧版本（action=rollback，version=0 回到内置默认）；expected 是页面上看到的生效版本。 */
export function switchPrompt(
  key: string,
  action: 'publish' | 'rollback',
  body: { version: number; expected: number; reason: string },
): Promise<{ prompt: PromptDetail; record: PromptPublishRecord }> {
  return request(`/admin/prompts/${encodeURIComponent(key)}/${action}`, {
    method: 'POST',
    body: JSON.stringify({ version: body.version, expected_active_version: body.expected, reason: body.reason }),
  });
}

export function getRiskOverview(): Promise<RiskOverview> {
  return request('/admin/risk/overview');
}

export function listAuditLogs(params: { targetType?: string; targetID?: string; operatorID?: string; page?: number; pageSize?: number }): Promise<Page<AuditLog>> {
  return request(
    `/admin/audit-logs${query({ target_type: params.targetType, target_id: params.targetID, operator_id: params.operatorID, page: params.page, page_size: params.pageSize })}`,
  );
}

export function listAdminRuns(params: { status?: RunStatus | ''; accountID?: string; page?: number; pageSize?: number }): Promise<Page<AdminRun>> {
  return request(`/admin/agent/runs${query({ status: params.status || undefined, account_id: params.accountID, page: params.page, page_size: params.pageSize })}`);
}

export function getAdminRun(id: string): Promise<AdminRunDetail> {
  return request(`/admin/agent/runs/${encodeURIComponent(id)}`);
}

export function getVectorStatus(): Promise<VectorStatus> {
  return request('/admin/vector/status');
}

export function listEvals(): Promise<EvalOverview> {
  return request('/admin/evals');
}

/** id 形如 `<报告目录>/<suite>`，两段分别编码。 */
export function getEvalReport(id: string): Promise<EvalReport> {
  return request(`/admin/evals/reports/${id.split('/').map(encodeURIComponent).join('/')}`);
}

export function evalIntent(q: string): Promise<{ query: string; plan: Record<string, unknown>; duration_ms: number }> {
  return request('/eval/intent', { method: 'POST', body: JSON.stringify({ query: q }) });
}

export function evalTool(kind: 'rag' | 'products', q: string): Promise<EvalToolResult> {
  return request(`/eval/${kind}`, { method: 'POST', body: JSON.stringify({ query: q }) });
}
