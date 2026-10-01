import { useState } from 'react';
import { ApiError } from '../api/http';
import { getProduct, listPromotionsFor, listReviews, listSkus } from '../api/catalog';
import { ProductImage } from '../components/ProductImage';
import { Empty, ErrorState, Loading } from '../components/StateView';
import { StockBadge } from '../components/StockBadge';
import { describePromotion, formatDate, formatMoney, hasDiscount, scopeLabel } from '../lib/format';
import { lastProductsHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { ProductDetail, PublicReview } from '../types/api';

export function ProductDetailPage({ id }: { id: string }) {
  const [product, reload] = useRequest(`product|${id}`, async () => {
    try {
      return { found: true as const, data: await getProduct(id) };
    } catch (err) {
      if (err instanceof ApiError && err.code === 'product_not_found') {
        return { found: false as const };
      }
      throw err;
    }
  });

  return (
    <section>
      <p>
        <a href={lastProductsHref()}>← 返回商品列表</a>
      </p>
      {product.status === 'loading' && <Loading />}
      {product.status === 'error' && <ErrorState message={product.message} onRetry={reload} />}
      {product.status === 'ok' && !product.data.found && <Empty text="商品不存在，或已下架" />}
      {product.status === 'ok' && product.data.found && <Detail p={product.data.data} />}
    </section>
  );
}

function Detail({ p }: { p: ProductDetail }) {
  const images = p.image_urls.length > 0 ? p.image_urls : [p.image_url];
  const [current, setCurrent] = useState(0);

  return (
    <article className="detail">
      <div className="detail-top">
        <div className="gallery">
          <ProductImage src={images[current] ?? ''} alt={p.name} className="gallery-main" />
          {images.length > 1 && (
            <div className="thumbs" role="group" aria-label="商品图片">
              {images.map((src, i) => (
                <button
                  key={src + i}
                  type="button"
                  className={`thumb ${i === current ? 'active' : ''}`}
                  aria-label={`第 ${i + 1} 张图片`}
                  aria-pressed={i === current}
                  onClick={() => setCurrent(i)}
                >
                  <ProductImage src={src} alt="" />
                </button>
              ))}
            </div>
          )}
        </div>
        <div className="summary">
          <h2 className="detail-name">{p.name}</h2>
          <p className="muted">
            {p.brand && <>{p.brand} · </>}
            {p.merchant_name}
          </p>
          <p className="price-row">
            <span className="price large">{formatMoney(p.price)}</span>
            {hasDiscount(p.price, p.market_price) && (
              <s className="market-price" aria-label={`原价 ${formatMoney(p.market_price)}`}>
                {formatMoney(p.market_price)}
              </s>
            )}
          </p>
          <p className="tag-row">
            <StockBadge status={p.stock_status} />
            <span className="muted small">库存 {p.stock_quantity}</span>
          </p>
          {p.tags.length > 0 && (
            <p className="tag-row">
              {p.tags.map((t) => (
                <span key={t} className="tag">
                  {t}
                </span>
              ))}
            </p>
          )}
          {p.recommend_reason && <p>{p.recommend_reason}</p>}
          {p.risk_notes.length > 0 && (
            <div className="notice" role="note">
              <strong>风险提示</strong>
              <ul>
                {p.risk_notes.map((n) => (
                  <li key={n}>{n}</li>
                ))}
              </ul>
            </div>
          )}
        </div>
      </div>

      <Promotions productId={p.product_id} />
      <Section title="卖点" items={p.selling_points} />
      <div className="two-col">
        <Section title="适合" items={p.suitable_for} />
        <Section title="不适合" items={p.not_suitable_for} />
      </div>
      {p.attributes.length > 0 && (
        <section className="block">
          <h3>参数</h3>
          <dl className="attrs">
            {p.attributes.map((a) => (
              <div key={a.key}>
                <dt>{a.key}</dt>
                <dd>
                  {a.value}
                  {a.unit && ` ${a.unit}`}
                </dd>
              </div>
            ))}
          </dl>
        </section>
      )}
      {p.description && (
        <section className="block">
          <h3>商品介绍</h3>
          <p className="prewrap">{p.description}</p>
        </section>
      )}
      <Skus productId={p.product_id} />
      <Reviews productId={p.product_id} />
    </article>
  );
}

function Section({ title, items }: { title: string; items: string[] }) {
  if (items.length === 0) return null;
  return (
    <section className="block">
      <h3>{title}</h3>
      <ul className="bullets">
        {items.map((s) => (
          <li key={s}>{s}</li>
        ))}
      </ul>
    </section>
  );
}

function Promotions({ productId }: { productId: string }) {
  const [state] = useRequest(`promotions|${productId}`, () => listPromotionsFor(productId));
  if (state.status !== 'ok' || state.data.items.length === 0) return null;
  return (
    <section className="block">
      <h3>优惠</h3>
      <ul className="promo-list">
        {state.data.items.map((p) => (
          <li key={p.promotion_id}>
            <span className="tag">{scopeLabel[p.scope]}</span>
            <strong>{describePromotion(p)}</strong>
            <span className="muted small">
              {p.name}（至 {formatDate(p.end_at)}）
            </span>
          </li>
        ))}
      </ul>
    </section>
  );
}

function Skus({ productId }: { productId: string }) {
  const [state, reload] = useRequest(`skus|${productId}`, () => listSkus(productId));
  return (
    <section className="block">
      <h3>规格</h3>
      {state.status === 'loading' && <Loading />}
      {state.status === 'error' && <ErrorState message={state.message} onRetry={reload} />}
      {state.status === 'ok' && state.data.items.length === 0 && <Empty text="暂无规格" />}
      {state.status === 'ok' && state.data.items.length > 0 && (
        <div className="table-wrap">
          <table>
            <thead>
              <tr>
                <th scope="col">规格</th>
                <th scope="col">属性</th>
                <th scope="col">价格</th>
                <th scope="col">库存</th>
              </tr>
            </thead>
            <tbody>
              {state.data.items.map((s) => (
                <tr key={s.sku_id}>
                  <td>
                    {s.sku_name}
                    {s.is_default && <span className="tag">默认</span>}
                  </td>
                  <td>
                    {Object.entries(s.specs)
                      .map(([k, v]) => `${k}：${v}`)
                      .join('；') || '—'}
                  </td>
                  <td className="nowrap">{formatMoney(s.price)}</td>
                  <td className="nowrap">
                    <StockBadge status={s.stock_status} /> {s.stock_quantity}
                  </td>
                </tr>
              ))}
            </tbody>
          </table>
        </div>
      )}
    </section>
  );
}

function Reviews({ productId }: { productId: string }) {
  const [pages, setPages] = useState(1);
  const [extra, setExtra] = useState<PublicReview[]>([]);
  const [loadingMore, setLoadingMore] = useState(false);
  const [moreError, setMoreError] = useState('');
  const [first, reload] = useRequest(`reviews|${productId}`, () => listReviews(productId, 1));

  async function loadMore() {
    setLoadingMore(true);
    setMoreError('');
    try {
      const next = await listReviews(productId, pages + 1);
      setExtra((prev) => [...prev, ...next.items]);
      setPages(pages + 1);
    } catch (err) {
      setMoreError(err instanceof ApiError ? err.message : '加载失败，请稍后重试');
    } finally {
      setLoadingMore(false);
    }
  }

  return (
    <section className="block">
      <h3>评价{first.status === 'ok' && `（${first.data.total}）`}</h3>
      {first.status === 'loading' && <Loading />}
      {first.status === 'error' && <ErrorState message={first.message} onRetry={reload} />}
      {first.status === 'ok' && first.data.total === 0 && <Empty text="暂无评价" />}
      {first.status === 'ok' && first.data.total > 0 && (
        <>
          <ul className="reviews">
            {[...first.data.items, ...extra].map((r) => (
              <li key={r.review_id}>
                <p>
                  <strong>{r.reviewer_name}</strong>{' '}
                  <span aria-label={`${r.rating} 星`}>
                    {'★'.repeat(r.rating)}
                    {'☆'.repeat(5 - r.rating)}
                  </span>{' '}
                  <span className="muted small">{formatDate(r.created_at)}</span>
                </p>
                <p className="prewrap">{r.content}</p>
                {r.tags.length > 0 && (
                  <p className="tag-row">
                    {r.tags.map((t) => (
                      <span key={t} className="tag">
                        {t}
                      </span>
                    ))}
                  </p>
                )}
                {r.merchant_reply && <p className="reply">商家回复：{r.merchant_reply}</p>}
              </li>
            ))}
          </ul>
          {moreError && <ErrorState message={moreError} />}
          {first.data.items.length + extra.length < first.data.total && (
            <button type="button" className="button" disabled={loadingMore} onClick={loadMore}>
              {loadingMore ? '加载中…' : '加载更多评价'}
            </button>
          )}
        </>
      )}
    </section>
  );
}
