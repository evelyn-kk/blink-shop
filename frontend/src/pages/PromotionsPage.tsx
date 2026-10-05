import { useState } from 'react';
import { ApiError } from '../api/http';
import { listPromotions, updatePromotion } from '../api/marketing';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { DataTable } from '../components/DataTable';
import { PageLinks } from '../components/PageLinks';
import { ErrorState, Loading } from '../components/StateView';
import { StatusBadge } from '../components/StatusBadge';
import { formatDateTime, scopeLabel } from '../lib/format';
import { notify } from '../lib/notice';
import { parsePage } from '../lib/paging';
import { promotionState } from '../lib/promotion';
import { promotionEditHref, promotionsHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { MerchantPromotion } from '../types/api';

const PAGE_SIZE = 20;

const filters: { value: string; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'active', label: '已启用' },
  { value: 'inactive', label: '已停用' },
];

// 本店促销：列表（状态筛选、分页、刷新）、新建、编辑、停用/启用。停用需要确认；没有删除，停用即可。
export function PromotionsPage({ query }: { query: URLSearchParams }) {
  const status = query.get('status') ?? '';
  const page = parsePage(query.get('page'));
  const [result, reload] = useRequest(`promotions|${status}|${page}`, () => listPromotions({ status, page, pageSize: PAGE_SIZE }));
  const [pending, setPending] = useState<MerchantPromotion | null>(null);
  const [busy, setBusy] = useState('');

  async function setStatus(p: MerchantPromotion, next: 'active' | 'inactive') {
    if (busy) return;
    setBusy(p.promotion_id);
    try {
      await updatePromotion(p.promotion_id, { status: next });
      notify('success', next === 'inactive' ? `已停用「${p.name}」` : `已启用「${p.name}」`);
    } catch (err) {
      notify('error', err instanceof ApiError ? err.message : '操作失败，请稍后重试');
    } finally {
      setBusy('');
      setPending(null);
      reload();
    }
  }

  return (
    <section aria-labelledby="promotions-title">
      <div className="page-head">
        <h2 id="promotions-title">促销</h2>
        <div className="mp-actions">
          <button type="button" className="button" onClick={reload}>
            刷新
          </button>
          <a className="button primary" href="#/merchant/promotions/new">
            新建促销
          </a>
        </div>
      </div>
      <p className="muted small">保存后立即参与购物车试算和下单；一个商品可以同时享受多个可叠加的促销，规则见购物车里的优惠明细。</p>
      <nav className="filter-tabs" aria-label="按状态筛选">
        {filters.map((f) => (
          <a
            key={f.value}
            className={`tab ${status === f.value ? 'active' : ''}`}
            aria-current={status === f.value ? 'page' : undefined}
            href={promotionsHref({ status: f.value })}
          >
            {f.label}
          </a>
        ))}
      </nav>

      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && (
        <>
          {result.data.total > 0 && <p className="muted">共 {result.data.total} 个促销</p>}
          <DataTable
            caption="本店促销"
            rows={result.data.items}
            rowKey={(p) => p.promotion_id}
            empty={status ? '没有符合条件的促销' : '还没有促销，新建一个吧'}
            columns={[
              { key: 'name', header: '名称', render: (p) => <strong className="break-all">{p.name}</strong> },
              {
                key: 'scope',
                header: '范围',
                render: (p) => (p.scope === 'merchant' ? '全店' : `${scopeLabel[p.scope]}：${p.target_name || p.product_id || p.category_id}`),
              },
              {
                key: 'rule',
                header: '规则',
                render: (p) => (
                  <>
                    {p.description}
                    {!p.stackable && <span className="tag">不可叠加</span>}
                  </>
                ),
              },
              {
                key: 'time',
                header: '时间',
                render: (p) => (
                  <span className="small">
                    {formatDateTime(p.start_at)} 至 {formatDateTime(p.end_at)}
                  </span>
                ),
              },
              {
                key: 'state',
                header: '状态',
                render: (p) => {
                  const s = promotionState(p);
                  return <StatusBadge tone={s.tone}>{s.label}</StatusBadge>;
                },
              },
              {
                key: 'actions',
                header: '操作',
                render: (p) => (
                  <span className="row-actions">
                    <a className="button" href={promotionEditHref(p.promotion_id)} aria-label={`编辑 ${p.name}`}>
                      编辑
                    </a>
                    {p.status === 'active' ? (
                      <button type="button" className="button danger-outline" aria-label={`停用 ${p.name}`} onClick={() => setPending(p)}>
                        停用
                      </button>
                    ) : (
                      <button
                        type="button"
                        className="button"
                        aria-label={`启用 ${p.name}`}
                        disabled={busy === p.promotion_id}
                        onClick={() => setStatus(p, 'active')}
                      >
                        {busy === p.promotion_id ? '处理中…' : '启用'}
                      </button>
                    )}
                  </span>
                ),
              },
            ]}
          />
          <PageLinks
            page={result.data.page}
            total={result.data.total}
            pageSize={result.data.page_size}
            href={(n) => promotionsHref({ status, page: n })}
          />
        </>
      )}

      <ConfirmDialog
        open={pending !== null}
        title="停用促销"
        message={pending ? `确定停用「${pending.name}」吗？停用后立即不再参与计价，可以随时重新启用。` : ''}
        confirmText="确认停用"
        danger
        busy={busy !== ''}
        onConfirm={() => pending && setStatus(pending, 'inactive')}
        onCancel={() => setPending(null)}
      />
    </section>
  );
}
