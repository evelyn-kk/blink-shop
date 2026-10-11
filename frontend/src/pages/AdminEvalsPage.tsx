import { useState, type FormEvent } from 'react';
import { evalIntent, evalTool, getEvalReport, getVectorStatus, listEvals } from '../api/admin';
import { ApiError } from '../api/http';
import { DataTable } from '../components/DataTable';
import { ErrorState, Loading } from '../components/StateView';
import { StatusBadge } from '../components/StatusBadge';
import { formatDateTime } from '../lib/format';
import { evalsHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { EvalOverview, EvalReport, EvalReportSummary, VectorIndexStatus, VectorStatus } from '../types/api';

const pct = (r: number) => `${(r * 100).toFixed(2)}%`;

function GateBadge({ gate }: { gate: 'passed' | 'failed' }) {
  return gate === 'passed' ? <StatusBadge tone="ok">通过</StatusBadge> : <StatusBadge tone="bad">未过门槛</StatusBadge>;
}

// 质量评测：评测套件（最新报告的通过率和门槛结论）、数据集、报告列表与详情（分组、失败样例），向量索引状态，以及单条试跑。
// 报告由 cmd/eval 生成，页面只读，不改动通过率；未过门槛的报告和失败的用例标红。
export function AdminEvalsPage({ query }: { query: URLSearchParams }) {
  const reportID = query.get('report') ?? '';
  const [overview, reload] = useRequest('admin-evals', listEvals);
  return (
    <section aria-labelledby="evals-title">
      <div className="page-head">
        <h2 id="evals-title">质量评测</h2>
        <button type="button" className="button" onClick={reload}>
          刷新
        </button>
      </div>
      {overview.status === 'loading' && <Loading />}
      {overview.status === 'error' && <ErrorState message={overview.message} onRetry={reload} />}
      {overview.status === 'ok' && <Overview o={overview.data} />}
      {reportID && <ReportDetail key={reportID} id={reportID} />}
      <TryPanel />
      <VectorPanel />
    </section>
  );
}

function Overview({ o }: { o: EvalOverview }) {
  if (!o.configured) return <p className="muted">服务没有配置评测目录（EVAL_DATA_DIR / EVAL_REPORT_DIR）。</p>;
  return (
    <>
      <h3>评测套件</h3>
      <div className="stat-grid">
        {o.suites.map((s) => (
          <div key={s.id} className={s.latest?.gate === 'failed' ? 'stat-card eval-failed' : 'stat-card'}>
            <h4>{s.name}</h4>
            <p className="muted small">
              {s.case_count} 条用例 · 门槛 {pct(s.min_pass_rate)}
            </p>
            {s.latest ? (
              <p className="tag-row">
                <GateBadge gate={s.latest.gate} />
                <span>
                  {s.latest.passed}/{s.latest.total}（{pct(s.latest.pass_rate)}）
                </span>
                <a href={evalsHref({ report: s.latest.id })} aria-label={`查看${s.name}最新报告`}>
                  最新报告
                </a>
              </p>
            ) : (
              <p className="muted small">还没有报告</p>
            )}
            <p className="code-inline">{s.command}</p>
          </div>
        ))}
      </div>
      <h3>报告</h3>
      {o.skipped > 0 && <p className="muted small">另有 {o.skipped} 份旧格式或无法读取的报告没有列出。</p>}
      <DataTable<EvalReportSummary>
        caption="评测报告"
        rows={o.reports}
        rowKey={(r) => r.id}
        empty="还没有报告，用 go run ./cmd/eval 生成"
        columns={[
          { key: 'time', header: '时间', render: (r) => formatDateTime(r.started_at) },
          { key: 'suite', header: '套件', render: (r) => r.suite },
          { key: 'version', header: '版本', render: (r) => <span className="code-inline">{r.version}</span> },
          { key: 'gate', header: '结论', render: (r) => <GateBadge gate={r.gate} /> },
          { key: 'rate', header: '通过率', align: 'end', render: (r) => `${r.passed}/${r.total}（${pct(r.pass_rate)}）` },
          { key: 'fail', header: '失败', align: 'end', render: (r) => r.failure_count },
          { key: 'open', header: '操作', render: (r) => <a className="button" href={evalsHref({ report: r.id })} aria-label={`查看报告 ${r.id}`}>查看</a> },
        ]}
      />
      <h3>数据集</h3>
      <DataTable
        caption="评测数据集"
        rows={o.datasets}
        rowKey={(d) => d.id}
        empty="数据目录下没有 *.jsonl"
        columns={[
          { key: 'id', header: '数据集', render: (d) => <span className="code-inline">{d.id}.jsonl</span> },
          { key: 'suite', header: '套件', render: (d) => d.suite || '—' },
          { key: 'count', header: '用例', align: 'end', render: (d) => d.case_count },
          { key: 'desc', header: '说明', render: (d) => <span className="small">{d.description || '—'}</span> },
          { key: 'time', header: '更新时间', render: (d) => formatDateTime(d.updated_at) },
        ]}
      />
    </>
  );
}

function ReportDetail({ id }: { id: string }) {
  const [report, reload] = useRequest(`eval-report|${id}`, () => getEvalReport(id));
  return (
    <section className="block" aria-labelledby="report-title">
      <h3 id="report-title" className="break-all">
        报告 {id}
      </h3>
      {report.status === 'loading' && <Loading />}
      {report.status === 'error' && <ErrorState message={report.message} onRetry={reload} />}
      {report.status === 'ok' && <ReportBody r={report.data} />}
    </section>
  );
}

function ReportBody({ r }: { r: EvalReport }) {
  const [onlyFailed, setOnlyFailed] = useState(r.gate === 'failed');
  const failures = new Map(r.failures.map((f) => [f.id, f]));
  const cases = onlyFailed ? r.cases.filter((c) => !c.passed) : r.cases;
  const groups = Object.entries(r.groups).sort(([a], [b]) => a.localeCompare(b));
  const metrics = Object.entries(r.metrics).sort(([a], [b]) => a.localeCompare(b));
  return (
    <>
      <p className="tag-row">
        <GateBadge gate={r.gate} />
        <span>
          {r.passed}/{r.total} 通过（{pct(r.pass_rate)}，门槛 {pct(r.min_pass_rate)}）
        </span>
        <span className="muted small">
          {formatDateTime(r.started_at)} · 版本 {r.version} · 配置指纹 {r.config_fingerprint} · {r.duration_ms} ms
        </span>
      </p>
      {metrics.length > 0 && (
        <dl className="stat-grid">
          {metrics.map(([k, v]) => (
            <div key={k} className="stat-card">
              <dt>{k}</dt>
              <dd>{v.toFixed(4)}</dd>
            </div>
          ))}
        </dl>
      )}
      <h4>分组</h4>
      <DataTable
        caption="分组通过情况"
        rows={groups}
        rowKey={([g]) => g}
        empty="没有分组"
        columns={[
          { key: 'g', header: '分组', render: ([g]) => g },
          {
            key: 'r',
            header: '通过',
            align: 'end',
            render: ([, v]) => (v.passed < v.total ? <StatusBadge tone="bad">{`${v.passed}/${v.total}`}</StatusBadge> : `${v.passed}/${v.total}`),
          },
        ]}
      />
      <h4>用例</h4>
      <label className="choice">
        <input type="checkbox" checked={onlyFailed} onChange={(e) => setOnlyFailed(e.target.checked)} />
        只看失败（{r.failures.length}）
      </label>
      {cases.length === 0 ? (
        <p className="muted">{onlyFailed ? '没有失败的用例' : '没有用例'}</p>
      ) : (
        <ul className="doc-list">
          {cases.map((c) => {
            const f = failures.get(c.id);
            return (
              <li key={c.id} className={c.passed ? 'doc-row' : 'doc-row eval-failed'}>
                <p className="tag-row">
                  <span className="code-inline">{c.id}</span>
                  <span className="muted small">{c.group}</span>
                  {c.passed ? <StatusBadge tone="ok">通过</StatusBadge> : <StatusBadge tone="bad">失败</StatusBadge>}
                </p>
                {f && (
                  <>
                    <p className="break-all">{f.query}</p>
                    <ul className="bullets">
                      {f.problems.map((p) => (
                        <li key={p}>{p}</li>
                      ))}
                    </ul>
                    {f.got.length > 0 && <p className="muted small break-all">实际：{f.got.join('；')}</p>}
                  </>
                )}
              </li>
            );
          })}
        </ul>
      )}
    </>
  );
}

type TryKind = 'intent' | 'rag' | 'products';
const tryKinds: { value: TryKind; label: string }[] = [
  { value: 'intent', label: '意图识别' },
  { value: 'rag', label: '知识检索' },
  { value: 'products', label: '商品搜索' },
];

// 单条试跑：与导购使用同一套规则和工具，同步返回，不保存。
function TryPanel() {
  const [kind, setKind] = useState<TryKind>('intent');
  const [text, setText] = useState('');
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState('');
  const [result, setResult] = useState<unknown>(null);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (busy || !text.trim()) return;
    setBusy(true);
    setError('');
    setResult(null);
    try {
      setResult(kind === 'intent' ? await evalIntent(text.trim()) : await evalTool(kind, text.trim()));
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '试跑失败，请稍后重试');
    } finally {
      setBusy(false);
    }
  }

  return (
    <section className="block" aria-labelledby="try-title">
      <h3 id="try-title">单条试跑</h3>
      <form className="search-form" onSubmit={submit}>
        <label className="field">
          <span>类型</span>
          <select value={kind} onChange={(e) => setKind(e.target.value as TryKind)}>
            {tryKinds.map((k) => (
              <option key={k.value} value={k.value}>
                {k.label}
              </option>
            ))}
          </select>
        </label>
        <label className="field">
          <span>输入</span>
          <input type="search" value={text} maxLength={500} onChange={(e) => setText(e.target.value)} aria-invalid={error ? true : undefined} />
        </label>
        <button type="submit" className="button primary" disabled={busy || !text.trim()}>
          {busy ? '试跑中…' : '试跑'}
        </button>
      </form>
      {error && (
        <p className="field-error" role="alert">
          {error}
        </p>
      )}
      {result !== null && <pre className="prompt-content" aria-label="试跑结果">{JSON.stringify(result, null, 2)}</pre>}
    </section>
  );
}

function VectorPanel() {
  const [st, reload] = useRequest('vector-status', getVectorStatus);
  return (
    <section className="block" aria-labelledby="vector-title">
      <div className="page-head">
        <h3 id="vector-title">向量索引</h3>
        <button type="button" className="button" onClick={reload}>
          刷新
        </button>
      </div>
      {st.status === 'loading' && <Loading />}
      {st.status === 'error' && <ErrorState message={st.message} onRetry={reload} />}
      {st.status === 'ok' && <VectorBody v={st.data} />}
    </section>
  );
}

function VectorBody({ v }: { v: VectorStatus }) {
  const rows: [string, VectorIndexStatus][] = [
    ['知识分块', v.knowledge],
    ['商品文本', v.products],
    ['商品图片', v.images],
  ];
  return (
    <>
      <DataTable
        caption="向量索引状态"
        rows={rows}
        rowKey={([name]) => name}
        empty=""
        columns={[
          { key: 'name', header: '索引', render: ([name]) => name },
          {
            key: 'state',
            header: '状态',
            render: ([, x]) => {
              if (!x.enabled) return <StatusBadge tone="muted">未启用</StatusBadge>;
              if (x.collection?.error) return <StatusBadge tone="bad">异常：{x.collection.error}</StatusBadge>;
              if (x.collection && !x.collection.exists) return <StatusBadge tone="warn">集合未建立</StatusBadge>;
              return <StatusBadge tone="ok">{x.backend === 'memory' ? '进程内存' : x.collection?.load_state || '正常'}</StatusBadge>;
            },
          },
          { key: 'coll', header: '集合', render: ([, x]) => <span className="code-inline">{x.collection?.name ?? '—'}</span> },
          {
            key: 'rows',
            header: '数量',
            align: 'end',
            render: ([, x]) => (x.collection ? `${x.collection.row_count} 行` : x.indexed_products !== undefined ? `${x.indexed_products} 个商品` : '—'),
          },
          { key: 'dim', header: '维度', align: 'end', render: ([, x]) => x.collection?.dimension || '—' },
          { key: 'emb', header: 'Embedder', render: ([, x]) => x.embedder || '—' },
        ]}
      />
      <p className={v.sync.failed > 0 ? 'small field-error' : 'muted small'}>
        商品向量同步（本进程）：成功 {v.sync.succeeded}，失败 {v.sync.failed}，进行中 {v.sync.running}
        {v.sync.last_at ? `，最近 ${formatDateTime(v.sync.last_at)}` : ''}
        {v.sync.last_error_at ? `，最近失败 ${formatDateTime(v.sync.last_error_at)}` : ''}
      </p>
    </>
  );
}
