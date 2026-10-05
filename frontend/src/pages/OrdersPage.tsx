import { useState, type FormEvent } from 'react';
import { listMerchants } from '../api/catalog';
import { listOrders, type OrderScope } from '../api/orders';
import { OrderActions } from '../components/OrderActions';
import { PageLinks } from '../components/PageLinks';
import { Empty, ErrorState, Loading } from '../components/StateView';
import { formatDateTime, formatMoney, orderStatusLabel, orderStatusTone } from '../lib/format';
import { parsePage } from '../lib/paging';
import { orderHref, ordersHref } from '../lib/router';
import { notify } from '../lib/notice';
import { StatusBadge } from '../components/StatusBadge';
import { useRequest } from '../lib/useRequest';

const PAGE_SIZE = 20;

const statusFilters: { value: string; label: string }[] = [
  { value: '', label: '全部' },
  { value: 'paid', label: '待发货' },
  { value: 'pending_payment', label: '待支付' },
  { value: 'shipped', label: '已发货' },
  { value: 'completed', label: '已完成' },
  { value: 'cancelled', label: '已取消' },
];

// 订单列表：商家看本店订单并发货；管理员看全部订单，可按店铺筛选，代发货或取消待支付订单。
export function OrdersPage({ scope, query }: { scope: OrderScope; query: URLSearchParams }) {
  const status = query.get('status') ?? '';
  const orderNo = query.get('order_no') ?? '';
  const merchantId = scope === 'admin' ? (query.get('merchant_id') ?? '') : '';
  const page = parsePage(query.get('page'));
  const [result, reload] = useRequest(`orders|${scope}|${status}|${orderNo}|${merchantId}|${page}`, () =>
    listOrders(scope, { status, orderNo, merchantId, page, pageSize: PAGE_SIZE }),
  );
  const [merchants] = useRequest(`merchants|${scope}`, () =>
    scope === 'admin' ? listMerchants() : Promise.resolve({ items: [], page: 1, page_size: 0, total: 0 }),
  );
  const href = (p: { status?: string; orderNo?: string; merchantId?: string; page?: number }) =>
    ordersHref(scope, { status, orderNo, merchantId, ...p });

  return (
    <section aria-labelledby="orders-title">
      <div className="page-head">
        <h2 id="orders-title">{scope === 'admin' ? '订单管理' : '订单'}</h2>
        <button type="button" className="button" onClick={reload}>
          刷新
        </button>
      </div>

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
        key={`${orderNo}|${merchantId}`}
        scope={scope}
        orderNo={orderNo}
        merchantId={merchantId}
        merchants={merchants.status === 'ok' ? merchants.data.items : []}
        onSubmit={(no, mid) => (window.location.hash = ordersHref(scope, { status, orderNo: no, merchantId: mid }))}
      />

      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && result.data.total === 0 && (
        <Empty text={status || orderNo || merchantId ? '没有符合条件的订单' : '还没有订单'} />
      )}
      {result.status === 'ok' && result.data.total > 0 && (
        <>
          <p className="muted">共 {result.data.total} 个订单</p>
          <ul className="doc-list">
            {result.data.items.map((o) => (
              <li key={o.order_id} className="doc-row order-row">
                <div className="doc-main">
                  <h3 className="doc-title">
                    <a href={orderHref(scope, o.order_id)}>订单 {o.order_no}</a>
                  </h3>
                  <p className="tag-row">
                    <StatusBadge tone={orderStatusTone[o.status]}>{orderStatusLabel[o.status]}</StatusBadge>
                    {scope === 'admin' && <span className="tag">{o.merchant_name || o.merchant_id}</span>}
                    <span className="muted small">下单于 {formatDateTime(o.created_at)}</span>
                  </p>
                  <p className="order-items-line" title={itemsText(o.items)}>
                    {itemsText(o.items)}
                  </p>
                  <p className="price">实付 {formatMoney(o.pay_amount)}</p>
                </div>
                <div className="mp-actions">
                  <OrderActions
                    scope={scope}
                    order={o}
                    onDone={(r) => {
                      notify(r.ok ? 'success' : 'error', r.message);
                      reload();
                    }}
                  />
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

function itemsText(items: { name: string; quantity: number }[]): string {
  return items.map((it) => `${it.name} ×${it.quantity}`).join('、');
}

function FilterForm(props: {
  scope: OrderScope;
  orderNo: string;
  merchantId: string;
  merchants: { merchant_id: string; name: string }[];
  onSubmit: (orderNo: string, merchantId: string) => void;
}) {
  const [orderNo, setOrderNo] = useState(props.orderNo);
  const [merchant, setMerchant] = useState(props.merchantId);
  function submit(e: FormEvent) {
    e.preventDefault();
    props.onSubmit(orderNo.trim(), merchant);
  }
  return (
    <form className="search-form" role="search" onSubmit={submit}>
      {props.scope === 'admin' && (
        <label className="field">
          <span>店铺</span>
          <select value={merchant} onChange={(e) => setMerchant(e.target.value)}>
            <option value="">全部店铺</option>
            {props.merchants.map((m) => (
              <option key={m.merchant_id} value={m.merchant_id}>
                {m.name}
              </option>
            ))}
          </select>
        </label>
      )}
      <label className="field">
        <span>订单号</span>
        <input type="search" value={orderNo} maxLength={32} onChange={(e) => setOrderNo(e.target.value)} />
      </label>
      <button type="submit" className="button">
        查找
      </button>
    </form>
  );
}
