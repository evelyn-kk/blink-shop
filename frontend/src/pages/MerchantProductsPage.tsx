import { useEffect, useState, type FormEvent } from 'react';
import { deleteMerchantProduct, listMerchantProducts } from '../api/merchant';
import { ApiError } from '../api/http';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { ProductImage } from '../components/ProductImage';
import { Empty, ErrorState, Loading } from '../components/StateView';
import { StockBadge } from '../components/StockBadge';
import { clearFlash, peekFlash } from '../lib/flash';
import { formatMoney, productStatusLabel } from '../lib/format';
import { parsePage } from '../lib/paging';
import { merchantProductEditHref, merchantProductsHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { MerchantProduct } from '../types/api';

const PAGE_SIZE = 20;

const filters: { value: string; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'active', label: '上架中' },
  { value: 'inactive', label: '已下架' },
  { value: 'risk', label: '风控审核中' },
];

export function MerchantProductsPage({ query }: { query: URLSearchParams }) {
  const status = query.get('status') ?? '';
  const keyword = query.get('keyword') ?? '';
  const page = parsePage(query.get('page'));
  const [flash, setFlash] = useState(peekFlash);
  useEffect(() => clearFlash(), []);
  const [version, setVersion] = useState(0);
  const [result, reload] = useRequest(`mp|${status}|${keyword}|${page}|${version}`, () =>
    listMerchantProducts({ status, keyword, page, pageSize: PAGE_SIZE }),
  );
  const [pendingDelete, setPendingDelete] = useState<MerchantProduct | null>(null);
  const [deleting, setDeleting] = useState(false);
  const [deleteError, setDeleteError] = useState('');

  async function confirmDelete() {
    if (!pendingDelete || deleting) return;
    setDeleting(true);
    setDeleteError('');
    try {
      await deleteMerchantProduct(pendingDelete.product_id);
      setFlash(`已删除「${pendingDelete.name}」`);
      setPendingDelete(null);
      setVersion((v) => v + 1);
    } catch (err) {
      setDeleteError(err instanceof ApiError ? err.message : '删除失败，请稍后重试');
      setPendingDelete(null);
    } finally {
      setDeleting(false);
    }
  }

  return (
    <section aria-labelledby="mp-title">
      <div className="page-head">
        <h2 id="mp-title">我的商品</h2>
        <a className="button primary" href="#/merchant/products/new">
          新建商品
        </a>
      </div>
      {flash && (
        <p className="flash" role="status">
          {flash}
        </p>
      )}
      {deleteError && <ErrorState message={deleteError} />}

      <nav className="filter-tabs" aria-label="按状态筛选">
        {filters.map((f) => (
          <a
            key={f.value}
            className={`tab ${status === f.value ? 'active' : ''}`}
            aria-current={status === f.value ? 'page' : undefined}
            href={merchantProductsHref({ status: f.value, keyword })}
          >
            {f.label}
          </a>
        ))}
      </nav>
      <KeywordForm key={keyword} keyword={keyword} status={status} />

      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && result.data.total === 0 && (
        <Empty text={status || keyword ? '没有符合条件的商品' : '还没有商品，先新建一个吧'} />
      )}
      {result.status === 'ok' && result.data.total > 0 && (
        <>
          <p className="muted">共 {result.data.total} 件</p>
          <ul className="mp-list">
            {result.data.items.map((p) => (
              <li key={p.product_id} className="mp-row">
                <ProductImage src={p.image_url} alt={p.name} className="mp-thumb" />
                <div className="mp-main">
                  <h3 className="product-name" title={p.name}>
                    {p.name}
                  </h3>
                  <p className="tag-row">
                    <span className={`status status-${p.status}`}>{productStatusLabel[p.status]}</span>
                    <StockBadge status={p.stock_status} />
                    <span className="muted small">
                      库存 {p.stock_quantity} · {p.skus.length} 个规格
                    </span>
                  </p>
                  <p className="price">{formatMoney(p.price)}</p>
                </div>
                <div className="mp-actions">
                  <a className="button" href={merchantProductEditHref(p.product_id)} aria-label={`编辑 ${p.name}`}>
                    编辑
                  </a>
                  <button
                    type="button"
                    className="button danger-outline"
                    aria-label={`删除 ${p.name}`}
                    onClick={() => setPendingDelete(p)}
                  >
                    删除
                  </button>
                </div>
              </li>
            ))}
          </ul>
          <MerchantPager
            page={result.data.page}
            total={result.data.total}
            pageSize={result.data.page_size}
            href={(n) => merchantProductsHref({ status, keyword, page: n })}
          />
        </>
      )}

      <ConfirmDialog
        open={pendingDelete !== null}
        title="删除商品"
        message={pendingDelete ? `确定删除「${pendingDelete.name}」吗？删除后买家将看不到这个商品，且无法恢复。` : ''}
        confirmText="确认删除"
        danger
        busy={deleting}
        onConfirm={confirmDelete}
        onCancel={() => setPendingDelete(null)}
      />
    </section>
  );
}

function KeywordForm({ keyword, status }: { keyword: string; status: string }) {
  const [value, setValue] = useState(keyword);
  function submit(e: FormEvent) {
    e.preventDefault();
    window.location.hash = merchantProductsHref({ status, keyword: value.trim() });
  }
  return (
    <form className="search-form" role="search" onSubmit={submit}>
      <label className="field">
        <span>按名称查找</span>
        <input type="search" value={value} maxLength={64} onChange={(e) => setValue(e.target.value)} />
      </label>
      <button type="submit" className="button">
        查找
      </button>
    </form>
  );
}

function MerchantPager(props: { page: number; total: number; pageSize: number; href: (n: number) => string }) {
  const last = Math.max(1, Math.ceil(props.total / Math.max(props.pageSize, 1)));
  if (last <= 1) return null;
  return (
    <nav className="pager" aria-label="分页">
      {props.page > 1 && (
        <a className="button" href={props.href(Math.min(props.page - 1, last))}>
          上一页
        </a>
      )}
      <span>
        第 {Math.min(props.page, last)} / {last} 页
      </span>
      {props.page < last && (
        <a className="button" href={props.href(props.page + 1)}>
          下一页
        </a>
      )}
    </nav>
  );
}
