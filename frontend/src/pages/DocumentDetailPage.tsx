import { getDocument, type DocumentScope } from '../api/knowledge';
import { ErrorState, Loading } from '../components/StateView';
import { docStatusLabel, docStatusTone, docTypeLabel, formatDateTime } from '../lib/format';
import { documentsHref } from '../lib/router';
import { StatusBadge } from '../components/StatusBadge';
import { useRequest } from '../lib/useRequest';

// 资料详情：状态、来源、清洗后的正文和切出的片段（导购检索和引用的最小单位）。
export function DocumentDetailPage({ scope, id }: { scope: DocumentScope; id: string }) {
  const [result, reload] = useRequest(`doc|${scope}|${id}`, () => getDocument(scope, id));

  return (
    <section aria-labelledby="doc-title">
      <p>
        <a href={documentsHref(scope)}>← 返回资料列表</a>
      </p>
      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && (
        <>
          <h2 id="doc-title" className="doc-detail-title">
            {result.data.title}
          </h2>
          <p className="tag-row">
            <StatusBadge tone={docStatusTone[result.data.status]}>{docStatusLabel[result.data.status]}</StatusBadge>
            <span className="tag">{docTypeLabel[result.data.doc_type] ?? result.data.doc_type}</span>
            {scope === 'admin' && <span className="tag">{result.data.merchant_id || '平台资料'}</span>}
          </p>
          {result.data.status === 'failed' && (
            <p className="form-error" role="alert">
              入库失败：{result.data.error_reason || '原因未知'}。重新提交相同内容即可重试。
            </p>
          )}
          <dl className="doc-meta">
            <dt>片段数</dt>
            <dd>{result.data.chunk_count}</dd>
            {result.data.product_id && (
              <>
                <dt>关联商品</dt>
                <dd>{result.data.product_id}</dd>
              </>
            )}
            {result.data.source_url && (
              <>
                <dt>来源网页</dt>
                <dd className="break-all">{result.data.source_url}</dd>
              </>
            )}
            <dt>更新时间</dt>
            <dd>{formatDateTime(result.data.updated_at)}</dd>
          </dl>

          <h3>片段（{result.data.chunks.length}）</h3>
          {result.data.chunks.length === 0 ? (
            <p className="muted">暂无片段</p>
          ) : (
            <ol className="chunk-list">
              {result.data.chunks.map((c) => (
                <li key={c.chunk_id} className="chunk">
                  <p className="chunk-title">{c.title}</p>
                  <p className="chunk-content">{c.content}</p>
                  <p className="muted small">{[...c.content].length} 字</p>
                </li>
              ))}
            </ol>
          )}
          <details className="doc-content">
            <summary>清洗后的全文</summary>
            <pre>{result.data.content}</pre>
          </details>
        </>
      )}
    </section>
  );
}
