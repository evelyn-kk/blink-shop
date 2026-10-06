import { useState, type FormEvent } from 'react';
import { listAuditLogs } from '../api/admin';
import { DataTable } from '../components/DataTable';
import { PageLinks } from '../components/PageLinks';
import { ErrorState, Loading } from '../components/StateView';
import { formatDateTime } from '../lib/format';
import { parsePage } from '../lib/paging';
import { auditHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';

const PAGE_SIZE = 20;

const targetTypes: { value: string; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'account', label: '账号' },
  { value: 'merchant', label: '店铺' },
  { value: 'product', label: '商品' },
  { value: 'promotion', label: '促销' },
  { value: 'review', label: '评价' },
  { value: 'config', label: '配置' },
];

// 操作审计：管理员的状态修改和配置修改，按时间倒序，可按对象类型和 ID 筛选（列表里的“记录”按钮直接带上筛选条件）。
export function AdminAuditPage({ query }: { query: URLSearchParams }) {
  const targetType = query.get('target_type') ?? '';
  const targetID = query.get('target_id') ?? '';
  const page = parsePage(query.get('page'));
  const [result, reload] = useRequest(`audit|${targetType}|${targetID}|${page}`, () => listAuditLogs({ targetType, targetID, page, pageSize: PAGE_SIZE }));

  return (
    <section aria-labelledby="audit-title">
      <div className="page-head">
        <h2 id="audit-title">操作审计</h2>
        <button type="button" className="button" onClick={reload}>
          刷新
        </button>
      </div>
      <Filter key={`${targetType}|${targetID}`} targetType={targetType} targetID={targetID} />
      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && (
        <>
          {result.data.total > 0 && <p className="muted">共 {result.data.total} 条</p>}
          <DataTable
            caption="操作审计"
            rows={result.data.items}
            rowKey={(l) => l.audit_id}
            empty="没有符合条件的操作记录"
            columns={[
              { key: 'time', header: '时间', render: (l) => formatDateTime(l.created_at) },
              { key: 'operator', header: '操作人', render: (l) => l.operator_name || l.operator_id },
              { key: 'action', header: '动作', render: (l) => l.action },
              { key: 'target', header: '对象', render: (l) => <span className="break-all">{l.target_type} {l.target_id}</span> },
              { key: 'change', header: '变化', render: (l) => <span className="break-all">{l.before_value || '—'} → {l.after_value || '—'}</span> },
              { key: 'reason', header: '原因', render: (l) => l.reason || '—' },
            ]}
          />
          <PageLinks page={result.data.page} total={result.data.total} pageSize={result.data.page_size}
            href={(n) => auditHref({ target_type: targetType, target_id: targetID, page: n })} />
        </>
      )}
    </section>
  );
}

function Filter(props: { targetType: string; targetID: string }) {
  const [type, setType] = useState(props.targetType);
  const [id, setID] = useState(props.targetID);
  function submit(e: FormEvent) {
    e.preventDefault();
    window.location.hash = auditHref({ target_type: type, target_id: id.trim() });
  }
  return (
    <form className="search-form" role="search" onSubmit={submit}>
      <label className="field">
        <span>对象类型</span>
        <select value={type} onChange={(e) => setType(e.target.value)}>
          {targetTypes.map((t) => (
            <option key={t.value} value={t.value}>
              {t.label}
            </option>
          ))}
        </select>
      </label>
      <label className="field">
        <span>对象 ID</span>
        <input type="search" value={id} maxLength={128} onChange={(e) => setID(e.target.value)} />
      </label>
      <button type="submit" className="button">
        查找
      </button>
    </form>
  );
}
