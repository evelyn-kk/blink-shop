import { useState, type FormEvent, type ReactNode } from 'react';
import { changeStatus, listAdmin, type AdminResource, type AdminRow } from '../api/admin';
import { ApiError } from '../api/http';
import { DataTable, type Column } from '../components/DataTable';
import { PageLinks } from '../components/PageLinks';
import { ReasonDialog } from '../components/ReasonDialog';
import { ErrorState, Loading } from '../components/StateView';
import { StatusBadge } from '../components/StatusBadge';
import { needsReason, statusLabel, statusTone } from '../lib/adminStatus';
import { formatDateTime, formatMoney, scopeLabel } from '../lib/format';
import { notify } from '../lib/notice';
import { parsePage } from '../lib/paging';
import { auditHref, platformHref } from '../lib/router';
import { useSession } from '../lib/session';
import { useRequest } from '../lib/useRequest';

const PAGE_SIZE = 20;

interface Action {
  to: string;
  label: string;
}

interface TabDef<R extends AdminResource> {
  label: string;
  /** 审计里的对象类型。 */
  target: string;
  statuses: string[];
  keyword?: string;
  id: (row: AdminRow[R]) => string;
  name: (row: AdminRow[R]) => string;
  status: (row: AdminRow[R]) => string;
  columns: Column<AdminRow[R]>[];
  actions: (row: AdminRow[R], me: string) => Action[];
  consequence: (to: string) => string;
}

const roleLabel: Record<string, string> = { user: '用户', merchant: '商家', admin: '管理员' };

function entityActions(status: string): Action[] {
  const all: Action[] = [
    { to: 'active', label: '恢复正常' },
    { to: 'inactive', label: '停用' },
    { to: 'risk', label: '设为风控' },
  ];
  return all.filter((a) => a.to !== status);
}

const tabs: { [R in AdminResource]: TabDef<R> } = {
  accounts: {
    label: '账号',
    target: 'account',
    statuses: ['active', 'inactive', 'risk'],
    keyword: '账号或名称',
    id: (a) => a.account_id,
    name: (a) => a.username,
    status: (a) => a.status,
    columns: [
      { key: 'name', header: '账号', render: (a) => <><strong className="break-all">{a.username}</strong><br /><span className="muted small">{a.display_name}</span></> },
      { key: 'role', header: '角色', render: (a) => roleLabel[a.role] ?? a.role },
      { key: 'created', header: '注册时间', render: (a) => formatDateTime(a.created_at) },
    ],
    // 不能修改自己的账号。
    actions: (a, me) => (a.account_id === me ? [] : entityActions(a.status)),
    consequence: (to) =>
      to === 'inactive' ? '停用后该账号只能查看自己的会话和退出，已登录的设备立即受影响。' : to === 'risk' ? '风控后该账号只能浏览，不能下单或修改资料。' : '恢复后该账号可以正常使用。',
  },
  merchants: {
    label: '店铺',
    target: 'merchant',
    statuses: ['active', 'inactive', 'risk'],
    keyword: '店铺名',
    id: (m) => m.merchant_id,
    name: (m) => m.name,
    status: (m) => m.status,
    columns: [
      { key: 'name', header: '店铺', render: (m) => <strong className="break-all">{m.name}</strong> },
      { key: 'phone', header: '客服电话', render: (m) => m.service_phone || '—' },
      { key: 'created', header: '入驻时间', render: (m) => formatDateTime(m.created_at) },
    ],
    actions: (m) => entityActions(m.status),
    consequence: (to) => (to === 'active' ? '恢复后店铺的商品、促销和资料重新对外可见。' : '店铺的商品、促销、店铺券和知识资料都会立即对外隐藏。'),
  },
  products: {
    label: '商品',
    target: 'product',
    statuses: ['active', 'inactive', 'risk', 'deleted'],
    keyword: '商品名称',
    id: (p) => p.product_id,
    name: (p) => p.name,
    status: (p) => p.status,
    columns: [
      { key: 'name', header: '商品', render: (p) => <strong className="break-all">{p.name}</strong> },
      { key: 'merchant', header: '店铺', render: (p) => p.merchant_name },
      { key: 'price', header: '价格', align: 'end', render: (p) => formatMoney(p.price) },
      { key: 'stock', header: '库存', align: 'end', render: (p) => p.stock_quantity },
    ],
    actions: (p) => {
      if (p.status === 'deleted') return [];
      const all: Action[] = [
        { to: 'active', label: '上架' },
        { to: 'inactive', label: '下架' },
        { to: 'risk', label: '设为风控' },
      ];
      return all.filter((a) => a.to !== p.status);
    },
    consequence: (to) => (to === 'active' ? '上架后商品重新对外可见。' : '商品会立即从商品列表、搜索和购物车中隐藏。'),
  },
  promotions: {
    label: '促销',
    target: 'promotion',
    statuses: ['active', 'inactive'],
    id: (p) => p.promotion_id,
    name: (p) => p.name,
    status: (p) => p.status,
    columns: [
      { key: 'name', header: '名称', render: (p) => <strong className="break-all">{p.name}</strong> },
      { key: 'scope', header: '范围', render: (p) => (p.scope === 'merchant' ? '全店' : `${scopeLabel[p.scope]}${p.target_name ? `：${p.target_name}` : ''}`) },
      { key: 'rule', header: '规则', render: (p) => p.description },
      { key: 'time', header: '时间', render: (p) => <span className="small">{formatDateTime(p.start_at)} 至 {formatDateTime(p.end_at)}</span> },
    ],
    actions: (p) => (p.status === 'active' ? [{ to: 'inactive', label: '停用' }] : [{ to: 'active', label: '启用' }]),
    consequence: (to) => (to === 'inactive' ? '停用后立即不再参与计价。' : '启用后在有效期内参与计价。'),
  },
  reviews: {
    label: '评价',
    target: 'review',
    statuses: ['visible', 'hidden'],
    id: (r) => r.review_id,
    name: (r) => `${r.product_name} 的评价`,
    status: (r) => r.status,
    columns: [
      { key: 'product', header: '商品', render: (r) => r.product_name },
      { key: 'content', header: '内容', render: (r) => <span className="break-all">{'★'.repeat(r.rating)} {r.content}</span> },
      { key: 'who', header: '评价人', render: (r) => <span className="small">{r.reviewer_name}<br />{r.account_id}</span> },
    ],
    actions: (r) => (r.status === 'visible' ? [{ to: 'hidden', label: '隐藏' }] : [{ to: 'visible', label: '恢复显示' }]),
    consequence: (to) => (to === 'hidden' ? '隐藏后买家看不到这条评价，商家仍能看到。' : '恢复后买家可以看到这条评价。'),
  },
};

const tabOrder: AdminResource[] = ['accounts', 'merchants', 'products', 'promotions', 'reviews'];

// 平台管理：账号、店铺、商品、促销、评价。按状态和关键词筛选；状态修改需要确认，停用/风控/隐藏要写原因，记入操作审计。
export function AdminPlatformPage({ query }: { query: URLSearchParams }) {
  const raw = query.get('tab') ?? 'accounts';
  const tab = (tabOrder as string[]).includes(raw) ? (raw as AdminResource) : 'accounts';
  return (
    <section aria-labelledby="platform-title">
      <div className="page-head">
        <h2 id="platform-title">平台管理</h2>
      </div>
      <nav className="filter-tabs" aria-label="管理对象">
        {tabOrder.map((t) => (
          <a key={t} className={`tab ${tab === t ? 'active' : ''}`} aria-current={tab === t ? 'page' : undefined} href={platformHref({ tab: t })}>
            {tabs[t].label}
          </a>
        ))}
      </nav>
      <ResourceTab key={tab} resource={tab} query={query} />
    </section>
  );
}

function ResourceTab<R extends AdminResource>({ resource, query }: { resource: R; query: URLSearchParams }) {
  const def = tabs[resource] as TabDef<R>;
  const status = query.get('status') ?? '';
  const keyword = query.get('keyword') ?? '';
  const page = parsePage(query.get('page'));
  const me = useSession()?.account.account_id ?? '';
  const [result, reload] = useRequest(`admin|${resource}|${status}|${keyword}|${page}`, () =>
    listAdmin(resource, { status, keyword: def.keyword ? keyword : undefined, page, page_size: PAGE_SIZE }),
  );
  const [pending, setPending] = useState<{ row: AdminRow[R]; action: Action } | null>(null);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const href = (p: { status?: string; keyword?: string; page?: number }) => platformHref({ tab: resource, status, keyword, ...p });

  async function confirm(reason: string) {
    if (!pending || busy) return;
    setBusy(true);
    setError('');
    try {
      await changeStatus(resource, def.id(pending.row), pending.action.to, reason);
      notify('success', `「${def.name(pending.row)}」已${pending.action.label}`);
      setPending(null);
      reload();
    } catch (err) {
      // 对话框里显示原因（例如状态已被别人改了），关闭后列表刷新成最新状态。
      setError(err instanceof ApiError ? err.message : '操作失败，请稍后重试');
    } finally {
      setBusy(false);
    }
  }

  const columns: Column<AdminRow[R]>[] = [
    ...def.columns,
    {
      key: 'status',
      header: '状态',
      render: (row) => <StatusBadge tone={statusTone[def.status(row)] ?? 'muted'}>{statusLabel[def.status(row)] ?? def.status(row)}</StatusBadge>,
    },
    {
      key: 'actions',
      header: '操作',
      render: (row): ReactNode => (
        <span className="row-actions">
          {def.actions(row, me).map((a) => (
            <button
              key={a.to}
              type="button"
              className={`button ${needsReason(a.to) ? 'danger-outline' : ''}`}
              aria-label={`${a.label} ${def.name(row)}`}
              onClick={() => (setError(''), setPending({ row, action: a }))}
            >
              {a.label}
            </button>
          ))}
          <a className="button" href={auditHref({ target_type: def.target, target_id: def.id(row) })} aria-label={`查看 ${def.name(row)} 的操作记录`}>
            记录
          </a>
        </span>
      ),
    },
  ];

  return (
    <>
      <nav className="filter-tabs" aria-label="按状态筛选">
        {['', ...def.statuses].map((s) => (
          <a key={s} className={`tab ${status === s ? 'active' : ''}`} aria-current={status === s ? 'page' : undefined} href={href({ status: s, page: 1 })}>
            {s ? statusLabel[s] : '全部'}
          </a>
        ))}
      </nav>
      <div className="search-row">
        {def.keyword && <KeywordForm key={keyword} label={`按${def.keyword}查找`} keyword={keyword} onSubmit={(kw) => (window.location.hash = href({ keyword: kw, page: 1 }))} />}
        <button type="button" className="button" onClick={reload}>
          刷新
        </button>
      </div>
      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && (
        <>
          {result.data.total > 0 && <p className="muted">共 {result.data.total} 条</p>}
          <DataTable caption={def.label} columns={columns} rows={result.data.items} rowKey={def.id} empty={status || keyword ? '没有符合条件的记录' : '还没有记录'} />
          <PageLinks page={result.data.page} total={result.data.total} pageSize={result.data.page_size} href={(n) => href({ page: n })} />
        </>
      )}
      <ReasonDialog
        open={pending !== null}
        title={pending ? `${pending.action.label}「${def.name(pending.row)}」` : ''}
        message={pending ? def.consequence(pending.action.to) : ''}
        confirmText={pending ? `确认${pending.action.label}` : '确认'}
        requireReason={pending ? needsReason(pending.action.to) : false}
        danger={pending ? needsReason(pending.action.to) : false}
        busy={busy}
        error={error}
        onConfirm={confirm}
        onCancel={() => {
          setPending(null);
          if (error) reload();
        }}
      />
    </>
  );
}

function KeywordForm(props: { label: string; keyword: string; onSubmit: (keyword: string) => void }) {
  const [value, setValue] = useState(props.keyword);
  function submit(e: FormEvent) {
    e.preventDefault();
    props.onSubmit(value.trim());
  }
  return (
    <form className="search-form" role="search" onSubmit={submit}>
      <label className="field">
        <span>{props.label}</span>
        <input type="search" value={value} maxLength={64} onChange={(e) => setValue(e.target.value)} />
      </label>
      <button type="submit" className="button">
        查找
      </button>
    </form>
  );
}
