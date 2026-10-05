import { useState, type FormEvent } from 'react';
import { ApiError } from '../api/http';
import { listMerchantReviews, replyReview } from '../api/marketing';
import { Empty, ErrorState, Loading } from '../components/StateView';
import { PageLinks } from '../components/PageLinks';
import { StatusBadge } from '../components/StatusBadge';
import { formatDateTime } from '../lib/format';
import { notify } from '../lib/notice';
import { parsePage } from '../lib/paging';
import { reviewsHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { MerchantReview } from '../types/api';

const PAGE_SIZE = 20;
const MAX_REPLY = 500;

const filters: { value: string; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'false', label: '未回复' },
  { value: 'true', label: '已回复' },
];

// 本店商品收到的评价：按是否已回复筛选，回复或修改回复。被平台隐藏的评价也能看到和回复（买家看不到）。
export function ReviewsPage({ query }: { query: URLSearchParams }) {
  const replied = query.get('replied') ?? '';
  const page = parsePage(query.get('page'));
  const [result, reload] = useRequest(`reviews|${replied}|${page}`, () => listMerchantReviews({ replied, page, pageSize: PAGE_SIZE }));

  return (
    <section aria-labelledby="reviews-title">
      <div className="page-head">
        <h2 id="reviews-title">评价</h2>
        <button type="button" className="button" onClick={reload}>
          刷新
        </button>
      </div>
      <nav className="filter-tabs" aria-label="按回复状态筛选">
        {filters.map((f) => (
          <a
            key={f.value}
            className={`tab ${replied === f.value ? 'active' : ''}`}
            aria-current={replied === f.value ? 'page' : undefined}
            href={reviewsHref({ replied: f.value })}
          >
            {f.label}
          </a>
        ))}
      </nav>
      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && result.data.total === 0 && <Empty text={replied === 'false' ? '没有待回复的评价' : '还没有评价'} />}
      {result.status === 'ok' && result.data.total > 0 && (
        <>
          <p className="muted">共 {result.data.total} 条评价</p>
          <ul className="doc-list">
            {result.data.items.map((r) => (
              <li key={r.review_id} className="doc-row review-row">
                <ReviewItem review={r} onReplied={reload} />
              </li>
            ))}
          </ul>
          <PageLinks page={result.data.page} total={result.data.total} pageSize={result.data.page_size} href={(n) => reviewsHref({ replied, page: n })} />
        </>
      )}
    </section>
  );
}

function ReviewItem({ review, onReplied }: { review: MerchantReview; onReplied: () => void }) {
  const [editing, setEditing] = useState(!review.merchant_reply);
  const [text, setText] = useState(review.merchant_reply);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const fieldID = `reply-${review.review_id}`;

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (saving) return;
    const reply = text.trim();
    if (!reply || [...reply].length > MAX_REPLY) {
      setError(`回复内容需要 1 到 ${MAX_REPLY} 个字`);
      return;
    }
    setSaving(true);
    setError('');
    try {
      await replyReview(review.review_id, reply);
      notify('success', `已回复「${review.product_name}」的评价`);
      onReplied();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '回复失败，请稍后重试');
    } finally {
      setSaving(false);
    }
  }

  return (
    // 回复成功后列表重新加载（加载期间整个列表卸载），这一条会以新的数据重新创建，回到“显示回复”的状态。
    <article aria-label={`${review.product_name} 的评价`}>
      <h3 className="doc-title">{review.product_name}</h3>
      <p className="tag-row">
        <span className="rating" aria-label={`${review.rating} 分（满分 5 分）`}>
          {'★'.repeat(review.rating)}
          {'☆'.repeat(5 - review.rating)}
        </span>
        <span className="muted small">
          {review.reviewer_name} · {formatDateTime(review.created_at)}
        </span>
        {review.status === 'hidden' && <StatusBadge tone="bad">已被平台隐藏</StatusBadge>}
      </p>
      <p className="review-content">{review.content}</p>
      {review.tags.length > 0 && (
        <p className="tag-row">
          {review.tags.map((t) => (
            <span key={t} className="tag">
              {t}
            </span>
          ))}
        </p>
      )}
      {review.merchant_reply && !editing && (
        <div className="merchant-reply">
          <p>
            <strong>商家回复：</strong>
            {review.merchant_reply}
          </p>
          <p className="muted small">
            {review.merchant_replied_at && `回复于 ${formatDateTime(review.merchant_replied_at)} · `}
            <button type="button" className="link-button" onClick={() => setEditing(true)}>
              修改回复
            </button>
          </p>
        </div>
      )}
      {editing && (
        <form className="reply-form" onSubmit={submit} noValidate>
          <label htmlFor={fieldID}>{review.merchant_reply ? '修改回复' : '回复'}</label>
          <textarea
            id={fieldID}
            rows={3}
            value={text}
            onChange={(e) => setText(e.target.value)}
            aria-invalid={error ? true : undefined}
            aria-describedby={`${fieldID}-count${error ? ` ${fieldID}-error` : ''}`}
          />
          <span id={`${fieldID}-count`} className="muted small">
            {[...text.trim()].length} / {MAX_REPLY}
          </span>
          {error && (
            <span id={`${fieldID}-error`} className="field-error" role="alert">
              {error}
            </span>
          )}
          <div className="reply-actions">
            {review.merchant_reply && (
              <button type="button" className="button" onClick={() => (setEditing(false), setText(review.merchant_reply), setError(''))}>
                取消
              </button>
            )}
            <button type="submit" className="button primary" disabled={saving} aria-busy={saving}>
              {saving ? '提交中…' : '提交回复'}
            </button>
          </div>
        </form>
      )}
    </article>
  );
}
