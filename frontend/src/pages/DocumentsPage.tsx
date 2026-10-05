import { useState, type FormEvent } from 'react';
import { listMerchants } from '../api/catalog';
import { listDocuments, type DocumentScope } from '../api/knowledge';
import { PageLinks } from '../components/PageLinks';
import { Empty, ErrorState, Loading } from '../components/StateView';
import { docStatusLabel, docStatusTone, docTypeLabel, formatDateTime } from '../lib/format';
import { parsePage } from '../lib/paging';
import { documentHref, documentsHref, newDocumentHref } from '../lib/router';
import { StatusBadge } from '../components/StatusBadge';
import { useRequest } from '../lib/useRequest';

const PAGE_SIZE = 20;

const statusFilters: { value: string; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'indexed', label: '已入库' },
  { value: 'failed', label: '入库失败' },
];

// 知识资料列表：商家看本店资料；管理员看全部，可按商家或“平台资料”筛选。
export function DocumentsPage({ scope, query }: { scope: DocumentScope; query: URLSearchParams }) {
  const status = query.get('status') ?? '';
  const keyword = query.get('keyword') ?? '';
  const merchantId = scope === 'admin' && query.has('merchant_id') ? (query.get('merchant_id') ?? '') : undefined;
  const page = parsePage(query.get('page'));
  const [result, reload] = useRequest(`docs|${scope}|${status}|${keyword}|${merchantId ?? '*'}|${page}`, () =>
    listDocuments(scope, { status, keyword, merchantId, page, pageSize: PAGE_SIZE }),
  );
  const [merchants] = useRequest(`merchants|${scope}`, () =>
    scope === 'admin' ? listMerchants() : Promise.resolve({ items: [], page: 1, page_size: 0, total: 0 }),
  );
  const merchantName = (id: string) => {
    if (id === '') return '平台';
    if (merchants.status !== 'ok') return id;
    return merchants.data.items.find((m) => m.merchant_id === id)?.name ?? id;
  };
  const href = (p: { status?: string; keyword?: string; merchantId?: string; page?: number }) =>
    documentsHref(scope, { status, keyword, merchantId, ...p });

  return (
    <section aria-labelledby="docs-title">
      <div className="page-head">
        <h2 id="docs-title">{scope === 'admin' ? '知识资料（全部）' : '知识资料'}</h2>
        <a className="button primary" href={newDocumentHref(scope)}>
          {scope === 'admin' ? '采集资料' : '添加资料'}
        </a>
      </div>
      <p className="muted small">导购助手回答问题时会检索这里已入库的资料，并在回复中注明出处。</p>

      <nav className="filter-tabs" aria-label="按状态筛选">
        {statusFilters.map((f) => (
          <a
            key={f.value}
            className={`tab ${status === f.value ? 'active' : ''}`}
            aria-current={status === f.value ? 'page' : undefined}
            href={href({ status: f.value, page: 1 })}
          >
            {f.label}
          </a>
        ))}
      </nav>
      <FilterForm
        key={`${keyword}|${merchantId ?? '*'}`}
        scope={scope}
        keyword={keyword}
        merchantId={merchantId}
        merchants={merchants.status === 'ok' ? merchants.data.items : []}
        onSubmit={(kw, mid) => (window.location.hash = documentsHref(scope, { status, keyword: kw, merchantId: mid }))}
      />

      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && result.data.total === 0 && (
        <Empty text={status || keyword || merchantId !== undefined ? '没有符合条件的资料' : '还没有资料，先添加一份吧'} />
      )}
      {result.status === 'ok' && result.data.total > 0 && (
        <>
          <p className="muted">共 {result.data.total} 份</p>
          <ul className="doc-list">
            {result.data.items.map((d) => (
              <li key={d.document_id} className="doc-row">
                <div className="doc-main">
                  <h3 className="doc-title">
                    <a href={documentHref(scope, d.document_id)} title={d.title}>
                      {d.title}
                    </a>
                  </h3>
                  <p className="tag-row">
                    <StatusBadge tone={docStatusTone[d.status]}>{docStatusLabel[d.status]}</StatusBadge>
                    <span className="tag">{docTypeLabel[d.doc_type] ?? d.doc_type}</span>
                    {scope === 'admin' && <span className="tag">{merchantName(d.merchant_id)}</span>}
                    <span className="muted small">
                      {d.chunk_count} 个片段 · 更新于 {formatDateTime(d.updated_at)}
                    </span>
                  </p>
                  {d.status === 'failed' && d.error_reason && <p className="field-error">{d.error_reason}</p>}
                </div>
              </li>
            ))}
          </ul>
          <PageLinks page={result.data.page} total={result.data.total} pageSize={result.data.page_size} href={(n) => href({ page: n })} />
        </>
      )}
    </section>
  );
}

function FilterForm(props: {
  scope: DocumentScope;
  keyword: string;
  merchantId?: string;
  merchants: { merchant_id: string; name: string }[];
  onSubmit: (keyword: string, merchantId?: string) => void;
}) {
  const [keyword, setKeyword] = useState(props.keyword);
  // 管理员的商家筛选：'*' 全部，'' 平台资料，其他为商家 ID。
  const [merchant, setMerchant] = useState(props.merchantId ?? '*');
  function submit(e: FormEvent) {
    e.preventDefault();
    props.onSubmit(keyword.trim(), props.scope === 'admin' && merchant !== '*' ? merchant : undefined);
  }
  return (
    <form className="search-form" role="search" onSubmit={submit}>
      {props.scope === 'admin' && (
        <label className="field">
          <span>归属</span>
          <select value={merchant} onChange={(e) => setMerchant(e.target.value)}>
            <option value="*">全部</option>
            <option value="">平台资料</option>
            {props.merchants.map((m) => (
              <option key={m.merchant_id} value={m.merchant_id}>
                {m.name}
              </option>
            ))}
          </select>
        </label>
      )}
      <label className="field">
        <span>按标题查找</span>
        <input type="search" value={keyword} maxLength={64} onChange={(e) => setKeyword(e.target.value)} />
      </label>
      <button type="submit" className="button">
        查找
      </button>
    </form>
  );
}
