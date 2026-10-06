import { useState } from 'react';
import { getRiskOverview, listAuditLogs, updateConfig } from '../api/admin';
import { ApiError } from '../api/http';
import { ErrorState, Loading } from '../components/StateView';
import { statusLabel } from '../lib/adminStatus';
import { formatDateTime } from '../lib/format';
import { notify } from '../lib/notice';
import { auditHref, platformHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { RiskOverview } from '../types/api';

const groups: { key: keyof Pick<RiskOverview, 'accounts' | 'merchants' | 'products'>; label: string; tab: string }[] = [
  { key: 'accounts', label: '账号', tab: 'accounts' },
  { key: 'merchants', label: '店铺', tab: 'merchants' },
  { key: 'products', label: '商品', tab: 'products' },
];

// 风控：账号/店铺/商品按状态的数量（点击进入对应列表），风险词配置，最近的操作审计。
export function AdminRiskPage() {
  const [overview, reload] = useRequest('risk-overview', getRiskOverview);
  const [recent, reloadRecent] = useRequest('risk-recent', () => listAuditLogs({ pageSize: 10 }));

  return (
    <section aria-labelledby="risk-title">
      <div className="page-head">
        <h2 id="risk-title">风控</h2>
        <button type="button" className="button" onClick={() => (reload(), reloadRecent())}>
          刷新
        </button>
      </div>
      {overview.status === 'loading' && <Loading />}
      {overview.status === 'error' && <ErrorState message={overview.message} onRetry={reload} />}
      {overview.status === 'ok' && (
        <>
          <div className="stat-grid">
            {groups.map((g) => (
              <section key={g.key} className="stat-card" aria-labelledby={`stat-${g.key}`}>
                <h3 id={`stat-${g.key}`}>{g.label}</h3>
                <dl>
                  {(['risk', 'inactive', 'active'] as const).map((s) => (
                    <div key={s}>
                      <dt>{statusLabel[s]}</dt>
                      <dd>
                        <a href={platformHref({ tab: g.tab, status: s })} aria-label={`${statusLabel[s]}的${g.label}：${overview.data[g.key][s] ?? 0}`}>
                          {overview.data[g.key][s] ?? 0}
                        </a>
                      </dd>
                    </div>
                  ))}
                </dl>
              </section>
            ))}
          </div>
          <BlockedWords key={overview.data.blocked_words.join(',')} overview={overview.data} onSaved={reload} />
        </>
      )}

      <h3>最近操作</h3>
      {recent.status === 'loading' && <Loading />}
      {recent.status === 'error' && <ErrorState message={recent.message} onRetry={reloadRecent} />}
      {recent.status === 'ok' && (
        <ul className="doc-list">
          {recent.data.items.length === 0 && <li className="muted">还没有操作记录</li>}
          {recent.data.items.map((l) => (
            <li key={l.audit_id} className="doc-row small">
              {formatDateTime(l.created_at)} · {l.operator_name || l.operator_id} · {l.action} · {l.target_type} {l.target_id} ·{' '}
              {l.before_value || '—'} → {l.after_value || '—'}
              {l.reason && `（${l.reason}）`}
            </li>
          ))}
        </ul>
      )}
      <p>
        <a href={auditHref()}>查看全部操作审计 →</a>
      </p>
    </section>
  );
}

function BlockedWords({ overview, onSaved }: { overview: RiskOverview; onSaved: () => void }) {
  const [text, setText] = useState(overview.blocked_words.join('\n'));
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);

  async function save() {
    if (saving) return;
    const words = text
      .split(/[\n,，]/)
      .map((w) => w.trim())
      .filter(Boolean);
    if (words.some((w) => [...w].length > 20)) {
      setError('每个风险词最多 20 个字');
      return;
    }
    setSaving(true);
    setError('');
    try {
      await updateConfig('risk.blocked_words', [...new Set(words)].join(','));
      notify('success', '风险词已保存，立即生效');
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '保存失败，请稍后重试');
    } finally {
      setSaving(false);
    }
  }

  return (
    <section className="form" aria-labelledby="words-title">
      <h3 id="words-title">风险词</h3>
      <p className="muted small">
        导购对话里出现这些词时拦截并记录（导购 Agent 接入后生效）。每行一个，最多 200 个。当前来源：
        {overview.blocked_words_source === 'env' ? '环境变量（只读）' : overview.blocked_words_source === 'dynamic' ? '后台设置' : '默认值'}
      </p>
      <label htmlFor="blocked-words">风险词</label>
      <textarea
        id="blocked-words"
        rows={6}
        value={text}
        readOnly={!overview.blocked_words_editable}
        onChange={(e) => setText(e.target.value)}
        aria-invalid={error ? true : undefined}
        aria-describedby={error ? 'words-error' : undefined}
      />
      {error && (
        <span id="words-error" className="field-error" role="alert">
          {error}
        </span>
      )}
      {overview.blocked_words_editable && (
        <div className="reply-actions">
          <button type="button" className="button primary" onClick={save} disabled={saving}>
            {saving ? '保存中…' : '保存风险词'}
          </button>
        </div>
      )}
    </section>
  );
}
