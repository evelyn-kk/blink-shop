import { useEffect, useState, type FormEvent } from 'react';
import { getCategoryTree, listProducts } from '../api/catalog';
import { ProductImage } from '../components/ProductImage';
import { Empty, ErrorState, Loading } from '../components/StateView';
import { StockBadge } from '../components/StockBadge';
import { formatMoney, hasDiscount } from '../lib/format';
import { productHref, productsHref, rememberProductsHash } from '../lib/router';
import { parsePage } from '../lib/paging';
import { useRequest } from '../lib/useRequest';
import type { CategoryNode } from '../types/api';

const PAGE_SIZE = 12;
function flatten(tree: CategoryNode[]): { id: string; label: string }[] {
  return tree.flatMap((root) => [
    { id: root.category_id, label: root.name },
    ...root.children.map((c) => ({ id: c.category_id, label: `\u3000${c.name}` })),
  ]);
}

export function ProductListPage({ query }: { query: URLSearchParams }) {
  const keyword = query.get('keyword') ?? '';
  const categoryId = query.get('category_id') ?? '';
  const page = parsePage(query.get('page'));
  const href = productsHref({ keyword, categoryId, page });

  useEffect(() => rememberProductsHash(href), [href]);

  const [categories] = useRequest('categories', getCategoryTree);
  const [result, reload] = useRequest(`products|${keyword}|${categoryId}|${page}`, () =>
    listProducts({ keyword, categoryId, page, pageSize: PAGE_SIZE }),
  );

  return (
    <section aria-labelledby="products-title">
      <h2 id="products-title">商品巡检</h2>
      {/* key 随查询变化，地址栏前进/后退时表单回到当前条件 */}
      <SearchForm
        key={`${keyword}|${categoryId}`}
        keyword={keyword}
        categoryId={categoryId}
        options={categories.status === 'ok' ? flatten(categories.data) : []}
      />

      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && result.data.total === 0 && (
        <Empty text={keyword || categoryId ? '没有找到符合条件的商品' : '暂无上架商品'}>
          {(keyword || categoryId) && (
            <a className="button" href={productsHref({})}>
              清除筛选
            </a>
          )}
        </Empty>
      )}
      {result.status === 'ok' && result.data.total > 0 && result.data.items.length === 0 && (
        // 页码超出范围：服务端返回空页但 total > 0，引导回最后一页，而不是显示无效的分页。
        <Empty text={`第 ${result.data.page} 页没有商品，共 ${result.data.total} 件`}>
          <a className="button" href={productsHref({ keyword, categoryId, page: lastPage(result.data.total, result.data.page_size) })}>
            回到最后一页
          </a>
        </Empty>
      )}
      {result.status === 'ok' && result.data.items.length > 0 && (
        <>
          <p className="muted" aria-live="polite">
            共 {result.data.total} 件商品
          </p>
          <ul className="product-grid">
            {result.data.items.map((p) => (
              <li key={p.product_id}>
                <a className="product-card" href={productHref(p.product_id)}>
                  <ProductImage src={p.image_url} alt={p.name} />
                  <div className="product-card-body">
                    <h3 className="product-name" title={p.name}>
                      {p.name}
                    </h3>
                    <p className="muted small">{p.merchant_name}</p>
                    <p className="price-row">
                      <span className="price">{formatMoney(p.price)}</span>
                      {hasDiscount(p.price, p.market_price) && (
                        <s className="market-price" aria-label={`原价 ${formatMoney(p.market_price)}`}>
                          {formatMoney(p.market_price)}
                        </s>
                      )}
                    </p>
                    <p className="tag-row">
                      <StockBadge status={p.stock_status} />
                      {p.tags.slice(0, 3).map((t) => (
                        <span key={t} className="tag">
                          {t}
                        </span>
                      ))}
                    </p>
                  </div>
                </a>
              </li>
            ))}
          </ul>
          {/* 分页以服务端返回的实际 page / page_size 为准 */}
          <Pager
            page={result.data.page}
            totalPages={lastPage(result.data.total, result.data.page_size)}
            href={(n) => productsHref({ keyword, categoryId, page: n })}
          />
        </>
      )}
    </section>
  );
}

function lastPage(total: number, pageSize: number): number {
  return Math.max(1, Math.ceil(total / Math.max(pageSize, 1)));
}

function SearchForm(props: { keyword: string; categoryId: string; options: { id: string; label: string }[] }) {
  const [keyword, setKeyword] = useState(props.keyword);
  const [categoryId, setCategoryId] = useState(props.categoryId);

  function submit(e: FormEvent) {
    e.preventDefault();
    window.location.hash = productsHref({ keyword: keyword.trim(), categoryId });
  }

  return (
    <form className="search-form" role="search" onSubmit={submit}>
      <label className="field">
        <span>关键词</span>
        <input
          type="search"
          value={keyword}
          maxLength={64}
          placeholder="名称、品牌、标签或卖点"
          onChange={(e) => setKeyword(e.target.value)}
        />
      </label>
      <label className="field">
        <span>分类</span>
        <select value={categoryId} onChange={(e) => setCategoryId(e.target.value)}>
          <option value="">全部分类</option>
          {props.options.map((o) => (
            <option key={o.id} value={o.id}>
              {o.label}
            </option>
          ))}
        </select>
      </label>
      <button type="submit" className="button primary">
        搜索
      </button>
    </form>
  );
}

function Pager({ page, totalPages, href }: { page: number; totalPages: number; href: (n: number) => string }) {
  if (totalPages <= 1) return null;
  return (
    <nav className="pager" aria-label="分页">
      {page > 1 ? (
        <a className="button" href={href(page - 1)}>
          上一页
        </a>
      ) : (
        <span className="button disabled" aria-disabled="true">
          上一页
        </span>
      )}
      <span>
        第 {page} / {totalPages} 页
      </span>
      {page < totalPages ? (
        <a className="button" href={href(page + 1)}>
          下一页
        </a>
      ) : (
        <span className="button disabled" aria-disabled="true">
          下一页
        </span>
      )}
    </nav>
  );
}
