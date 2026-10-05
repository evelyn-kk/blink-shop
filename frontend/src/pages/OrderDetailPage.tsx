import { getOrder, type OrderScope } from '../api/orders';
import { OrderActions } from '../components/OrderActions';
import { ProductImage } from '../components/ProductImage';
import { ErrorState, Loading } from '../components/StateView';
import { formatDateTime, formatMoney, orderStatusLabel, orderStatusTone, paymentMethodLabel, paymentStatusLabel } from '../lib/format';
import { ordersHref } from '../lib/router';
import { notify } from '../lib/notice';
import { StatusBadge } from '../components/StatusBadge';
import { useRequest } from '../lib/useRequest';

// 订单详情：状态与时间线、买家、支付单、下单时冻结的商品信息和金额。
export function OrderDetailPage({ scope, id }: { scope: OrderScope; id: string }) {
  const [result, reload] = useRequest(`order|${scope}|${id}`, () => getOrder(scope, id));

  return (
    <section aria-labelledby="order-title">
      <p>
        <a href={ordersHref(scope)}>← 返回订单列表</a>
      </p>
      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && (
        <>
          <div className="page-head">
            <h2 id="order-title" className="doc-detail-title">
              订单 {result.data.order_no}
            </h2>
            <div className="mp-actions">
              <OrderActions
                scope={scope}
                order={result.data}
                onDone={(r) => {
                  notify(r.ok ? 'success' : 'error', r.message);
                  reload();
                }}
              />
            </div>
          </div>
          <dl className="doc-meta">
            <dt>状态</dt>
            <dd>
              <StatusBadge tone={orderStatusTone[result.data.status]}>{orderStatusLabel[result.data.status]}</StatusBadge>
            </dd>
            <dt>店铺</dt>
            <dd>{result.data.merchant_name || result.data.merchant_id}</dd>
            <dt>买家账号</dt>
            <dd className="break-all">{result.data.account_id}</dd>
            <dt>下单时间</dt>
            <dd>{formatDateTime(result.data.created_at)}</dd>
            {result.data.status === 'pending_payment' && result.data.payment_deadline_at && (
              <>
                <dt>支付期限</dt>
                <dd>{formatDateTime(result.data.payment_deadline_at)}</dd>
              </>
            )}
            {result.data.paid_at && (
              <>
                <dt>支付时间</dt>
                <dd>{formatDateTime(result.data.paid_at)}</dd>
              </>
            )}
            {result.data.shipped_at && (
              <>
                <dt>发货时间</dt>
                <dd>{formatDateTime(result.data.shipped_at)}</dd>
              </>
            )}
            {result.data.completed_at && (
              <>
                <dt>收货时间</dt>
                <dd>{formatDateTime(result.data.completed_at)}</dd>
              </>
            )}
            {result.data.closed_at && (
              <>
                <dt>关闭时间</dt>
                <dd>{formatDateTime(result.data.closed_at)}</dd>
                <dt>关闭原因</dt>
                <dd>{result.data.cancel_reason || '—'}</dd>
              </>
            )}
            {result.data.payment && (
              <>
                <dt>支付单</dt>
                <dd>
                  {paymentStatusLabel[result.data.payment.status]}
                  {result.data.payment.method && ` · ${paymentMethodLabel[result.data.payment.method] ?? result.data.payment.method}`}
                  {result.data.payment.transaction_no && <span className="muted small break-all"> · 流水号 {result.data.payment.transaction_no}</span>}
                </dd>
              </>
            )}
          </dl>

          <h3>商品</h3>
          <ul className="mp-list">
            {result.data.items.map((it) => (
              <li key={it.order_item_id} className="mp-row">
                <ProductImage src={it.image_url} alt={it.name} className="mp-thumb" />
                <div className="mp-main">
                  <h4 className="product-name" title={it.name}>
                    {it.name}
                  </h4>
                  <p className="muted small">
                    {it.sku_name} · {formatMoney(it.price)} × {it.quantity}
                    {result.data.status === 'completed' && (it.review_id ? ' · 已评价' : ' · 未评价')}
                  </p>
                </div>
                <p className="price">{formatMoney(it.amount)}</p>
              </li>
            ))}
          </ul>
          <dl className="doc-meta">
            <dt>商品金额</dt>
            <dd>{formatMoney(result.data.total_amount)}</dd>
            <dt>优惠</dt>
            <dd>−{formatMoney(result.data.discount_amount)}</dd>
            <dt>实付</dt>
            <dd className="price">{formatMoney(result.data.pay_amount)}</dd>
          </dl>
        </>
      )}
    </section>
  );
}
