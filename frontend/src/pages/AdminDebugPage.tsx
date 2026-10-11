import { useState, type FormEvent } from 'react';
import { getAdminRun, listAdminRuns } from '../api/admin';
import { DataTable } from '../components/DataTable';
import { PageLinks } from '../components/PageLinks';
import { ErrorState, Loading } from '../components/StateView';
import { StatusBadge, type BadgeTone } from '../components/StatusBadge';
import { formatDateTime } from '../lib/format';
import { parsePage } from '../lib/paging';
import { debugHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { AdminRun, AdminRunDetail, RunStatus, TraceEvent } from '../types/api';

const PAGE_SIZE = 20;

const runStatus: Record<RunStatus, [string, BadgeTone]> = {
  queued: ['排队中', 'muted'],
  running: ['运行中', 'info'],
  completed: ['已完成', 'ok'],
  failed: ['失败', 'bad'],
  cancelled: ['已取消', 'warn'],
};

// 轨迹事件的状态：ok 正常；blocked 风控拦截；skipped 跳过；error / failed 出错（计入失败数）。
function eventTone(status: string): BadgeTone {
  if (status === 'ok') return 'ok';
  if (status === 'error' || status === 'failed') return 'bad';
  if (status === 'blocked') return 'warn';
  return 'muted';
}

function elapsed(ms: number | null): string {
  if (ms === null) return '—';
  return ms < 1000 ? `${ms} ms` : `${(ms / 1000).toFixed(1)} s`;
}

// 调试观测：全部导购运行（按状态 / 账号筛选），点开看一次运行的回答、汇总和轨迹时间线（规划、检索、工具、模型、耗时、错误）。
// 提问和回答经服务端脱敏，轨迹不含工具的私密参数原文。
export function AdminDebugPage({ query }: { query: URLSearchParams }) {
  const status = (query.get('status') ?? '') as RunStatus | '';
  const accountID = query.get('account_id') ?? '';
  const page = parsePage(query.get('page'));
  const runID = query.get('run') ?? '';
  const [list, reload] = useRequest(`admin-runs|${status}|${accountID}|${page}`, () => listAdminRuns({ status, accountID, page, pageSize: PAGE_SIZE }));
  const href = (p: { page?: number; run?: string }) => debugHref({ status, account_id: accountID, page: p.page ?? page, run: p.run });

  return (
    <section aria-labelledby="debug-title">
      <div className="page-head">
        <h2 id="debug-title">调试观测</h2>
        <button type="button" className="button" onClick={reload}>
          刷新
        </button>
      </div>
      <RunFilter key={`${status}|${accountID}`} status={status} accountID={accountID} />
      {list.status === 'loading' && <Loading />}
      {list.status === 'error' && <ErrorState message={list.message} onRetry={reload} />}
      {list.status === 'ok' && (
        <>
          {list.data.total > 0 && <p className="muted">共 {list.data.total} 次运行</p>}
          <DataTable<AdminRun>
            caption="导购运行"
            rows={list.data.items}
            rowKey={(r) => r.run_id}
            empty="没有符合条件的运行"
            columns={[
              { key: 'time', header: '时间', render: (r) => formatDateTime(r.created_at) },
              { key: 'user', header: '用户', render: (r) => r.username || r.account_id },
              { key: 'query', header: '提问', render: (r) => <span className="break-all">{r.query || '—'}{r.attachments > 0 ? `（附图 ${r.attachments}）` : ''}</span> },
              { key: 'status', header: '状态', render: (r) => <StatusBadge tone={runStatus[r.status][1]}>{runStatus[r.status][0]}</StatusBadge> },
              { key: 'elapsed', header: '耗时', align: 'end', render: (r) => elapsed(r.elapsed_ms) },
              {
                key: 'open',
                header: '操作',
                render: (r) => (
                  <a className="button" href={href({ run: r.run_id })} aria-label={`查看运行 ${r.run_id}`} aria-current={r.run_id === runID ? 'true' : undefined}>
                    时间线
                  </a>
                ),
              },
            ]}
          />
          <PageLinks page={list.data.page} total={list.data.total} pageSize={list.data.page_size} href={(n) => href({ page: n })} />
        </>
      )}
      {runID && <RunDetail key={runID} runID={runID} />}
    </section>
  );
}

function RunFilter(props: { status: string; accountID: string }) {
  const [status, setStatus] = useState(props.status);
  const [account, setAccount] = useState(props.accountID);
  function submit(e: FormEvent) {
    e.preventDefault();
    window.location.hash = debugHref({ status, account_id: account.trim() });
  }
  return (
    <form className="search-form" role="search" onSubmit={submit}>
      <label className="field">
        <span>状态</span>
        <select value={status} onChange={(e) => setStatus(e.target.value)}>
          <option value="">全部</option>
          {(Object.keys(runStatus) as RunStatus[]).map((s) => (
            <option key={s} value={s}>
              {runStatus[s][0]}
            </option>
          ))}
        </select>
      </label>
      <label className="field">
        <span>账号 ID</span>
        <input type="search" value={account} maxLength={64} onChange={(e) => setAccount(e.target.value)} />
      </label>
      <button type="submit" className="button">
        查找
      </button>
    </form>
  );
}

function RunDetail({ runID }: { runID: string }) {
  const [detail, reload] = useRequest(`admin-run|${runID}`, () => getAdminRun(runID));
  return (
    <section className="block" aria-labelledby="run-title">
      <h3 id="run-title" className="break-all">
        运行 {runID}
      </h3>
      {detail.status === 'loading' && <Loading />}
      {detail.status === 'error' && <ErrorState message={detail.message} onRetry={reload} />}
      {detail.status === 'ok' && <RunBody d={detail.data} />}
    </section>
  );
}

function RunBody({ d }: { d: AdminRunDetail }) {
  const s = d.summary;
  const prompts = Object.entries(d.prompt_versions);
  return (
    <>
      <p className="tag-row">
        <StatusBadge tone={runStatus[d.status][1]}>{runStatus[d.status][0]}</StatusBadge>
        {d.error_code && <StatusBadge tone="bad">错误：{d.error_code}</StatusBadge>}
        <span className="muted small">
          {d.username || d.account_id} · {formatDateTime(d.created_at)} · 耗时 {elapsed(d.elapsed_ms)} · trace {d.trace_id}
        </span>
      </p>
      <dl className="stat-grid">
        <Stat label="事件" value={s.event_count} />
        <Stat label="失败事件" value={s.failed_count} bad={s.failed_count > 0} />
        <Stat label="事件耗时合计" value={`${s.events_ms} ms`} />
        <Stat label="模型" value={s.models.join('、') || '未用模型'} />
        <Stat label="工具" value={s.tools.join('、') || '—'} />
        <Stat label="检索分块" value={s.chunk_ids.length} />
      </dl>
      {prompts.length > 0 && (
        <p className="muted small">Prompt 版本：{prompts.map(([k, v]) => `${k} ${v === 0 ? '内置' : `v${v}`}`).join('，')}</p>
      )}
      <h4>提问</h4>
      <p className="prewrap break-all">{d.query || '—'}</p>
      <details>
        <summary>回答（{d.blocks.length} 个结构化块）</summary>
        <pre className="prompt-content">{d.content || '（没有文字回答）'}</pre>
        {d.blocks.length > 0 && <pre className="prompt-content">{JSON.stringify(d.blocks, null, 2)}</pre>}
      </details>
      {s.chunk_ids.length > 0 && <p className="muted small break-all">引用的分块：{s.chunk_ids.join('、')}</p>}
      <h4>时间线</h4>
      {d.items.length === 0 ? <p className="muted">没有轨迹事件</p> : (
        <ol className="timeline">
          {d.items.map((e) => (
            <TimelineItem key={e.trace_event_id} e={e} />
          ))}
        </ol>
      )}
    </>
  );
}

function Stat({ label, value, bad }: { label: string; value: string | number; bad?: boolean }) {
  return (
    <div className={bad ? 'stat-card stat-bad' : 'stat-card'}>
      <dt>{label}</dt>
      <dd>{value}</dd>
    </div>
  );
}

function TimelineItem({ e }: { e: TraceEvent }) {
  const meta = Object.keys(e.metadata).length > 0;
  return (
    <li className={`timeline-item tone-border-${eventTone(e.status)}`}>
      <div className="timeline-head">
        <strong>
          {e.stage} · {e.event_type}
        </strong>
        <StatusBadge tone={eventTone(e.status)}>{e.status}</StatusBadge>
        <span className="muted small">
          {e.duration_ms} ms{e.model ? ` · ${e.model}` : ''}
        </span>
      </div>
      {e.error && (
        <p className="field-error" role="note">
          {e.error}
        </p>
      )}
      {meta && (
        <details>
          <summary>详情</summary>
          <pre className="prompt-content">{JSON.stringify(e.metadata, null, 2)}</pre>
        </details>
      )}
    </li>
  );
}
