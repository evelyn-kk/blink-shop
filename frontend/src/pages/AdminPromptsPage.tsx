import { useState } from 'react';
import { getPrompt, listPrompts, savePromptDraft, switchPrompt } from '../api/admin';
import { ApiError } from '../api/http';
import { DataTable } from '../components/DataTable';
import { ReasonDialog } from '../components/ReasonDialog';
import { ErrorState, Loading } from '../components/StateView';
import { StatusBadge, type BadgeTone } from '../components/StatusBadge';
import { formatDateTime } from '../lib/format';
import { notify } from '../lib/notice';
import { promptsHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { PromptDetail, PromptPublishRecord, PromptSummary, PromptVersion } from '../types/api';

const statusLabel: Record<PromptVersion['status'], [string, BadgeTone]> = {
  draft: ['草稿', 'info'],
  active: ['生效中', 'ok'],
  archived: ['历史版本', 'muted'],
};

const resultLabel: Record<PromptPublishRecord['result'], [string, BadgeTone]> = {
  success: ['成功', 'ok'],
  fallback: ['已生效，配置中心推送失败', 'warn'],
  pending: ['处理中', 'muted'],
};

function versionLabel(v: number): string {
  return v === 0 ? '内置默认' : `v${v}`;
}

function activeLabel(p: PromptSummary): string {
  return p.active_source === 'builtin' ? '内置默认' : `v${p.active_version}`;
}

// Prompt 管理：导购 Agent 各环节的 Prompt。编辑先存草稿（不影响运行），发布后立即生效；可以回滚到历史版本或内置默认。
// 发布和回滚都要带上页面看到的生效版本，别人先改了会提示刷新，不会互相覆盖。
export function AdminPromptsPage({ query }: { query: URLSearchParams }) {
  const [list, reloadList] = useRequest('admin-prompts', listPrompts);
  const selected = query.get('key') ?? '';
  return (
    <section aria-labelledby="prompts-title">
      <div className="page-head">
        <h2 id="prompts-title">Prompt 管理</h2>
        <button type="button" className="button" onClick={reloadList}>
          刷新
        </button>
      </div>
      <p className="muted small">草稿保存后不影响运行；发布或回滚后本实例立即生效，其他实例在缓存刷新后（默认 10 秒内）生效。运行轨迹会记下用到的版本。</p>
      {list.status === 'loading' && <Loading />}
      {list.status === 'error' && <ErrorState message={list.message} onRetry={reloadList} />}
      {list.status === 'ok' && <PromptTabs items={list.data.items} selected={selected} onChanged={reloadList} />}
    </section>
  );
}

function PromptTabs({ items, selected, onChanged }: { items: PromptSummary[]; selected: string; onChanged: () => void }) {
  const current = items.find((p) => p.prompt_key === selected) ?? items[0];
  if (!current) return <p className="muted">没有可管理的 Prompt</p>;
  return (
    <>
      <nav className="filter-tabs" aria-label="选择 Prompt">
        {items.map((p) => (
          <a key={p.prompt_key} href={promptsHref(p.prompt_key)} className={p.prompt_key === current.prompt_key ? 'tab active' : 'tab'}
            aria-current={p.prompt_key === current.prompt_key ? 'page' : undefined}>
            {p.title}（{activeLabel(p)}{p.draft ? '，有草稿' : ''}）
          </a>
        ))}
      </nav>
      <PromptPanel key={current.prompt_key} promptKey={current.prompt_key} onChanged={onChanged} />
    </>
  );
}

function PromptPanel({ promptKey, onChanged }: { promptKey: string; onChanged: () => void }) {
  const [detail, reload] = useRequest(`admin-prompt|${promptKey}`, () => getPrompt(promptKey));
  if (detail.status === 'loading') return <Loading />;
  if (detail.status === 'error') return <ErrorState message={detail.message} onRetry={reload} />;
  const changed = () => {
    reload();
    onChanged();
  };
  return <PromptDetailView key={detailKey(detail.data)} detail={detail.data} onChanged={changed} />;
}

// 详情变化（保存草稿、发布、回滚）后重建编辑区，编辑框回到服务端的内容。
function detailKey(d: PromptDetail): string {
  return `${d.active_version}|${d.draft?.version ?? 0}|${d.draft?.updated_at ?? ''}|${d.publish_records[0]?.record_id ?? ''}`;
}

type Pending = { action: 'publish' | 'rollback'; version: number } | null;

function PromptDetailView({ detail, onChanged }: { detail: PromptDetail; onChanged: () => void }) {
  const active = detail.versions.find((v) => v.status === 'active');
  const activeContent = active?.content ?? detail.builtin_content;
  // 摘要里的 draft 不带内容，内容在 versions 里
  const draft = detail.versions.find((v) => v.status === 'draft') ?? null;
  const [content, setContent] = useState(draft?.content ?? activeContent);
  const [saving, setSaving] = useState(false);
  const [draftError, setDraftError] = useState('');
  const [pending, setPending] = useState<Pending>(null);
  const [busy, setBusy] = useState(false);
  const [switchError, setSwitchError] = useState('');
  const dirty = content.trim() !== (draft?.content ?? activeContent).trim();

  async function saveDraft() {
    if (saving) return;
    setSaving(true);
    setDraftError('');
    try {
      const saved = await savePromptDraft(detail.prompt_key, content);
      notify('success', `已保存草稿 v${saved.version}`);
      onChanged();
    } catch (err) {
      setDraftError(err instanceof ApiError ? err.message : '保存失败，请稍后重试');
    } finally {
      setSaving(false);
    }
  }

  async function confirmSwitch(reason: string) {
    if (!pending || busy) return;
    setBusy(true);
    setSwitchError('');
    try {
      const { record } = await switchPrompt(detail.prompt_key, pending.action, { version: pending.version, expected: detail.active_version, reason });
      const done = pending.action === 'publish' ? `已发布 v${record.version}` : `已回滚到${versionLabel(record.version)}`;
      notify(record.result === 'fallback' ? 'info' : 'success', record.result === 'fallback' ? `${done}，已生效，但推送配置中心失败` : done);
      setPending(null);
      onChanged();
    } catch (err) {
      setSwitchError(err instanceof ApiError ? err.message : '操作失败，请稍后重试');
    } finally {
      setBusy(false);
    }
  }

  function cancelSwitch() {
    setPending(null);
    // 生效版本被别人改了：关掉对话框后刷新，让管理员看到最新状态再决定
    if (switchError) onChanged();
    setSwitchError('');
  }

  const editorID = `prompt-editor-${detail.prompt_key.replace(/\W/g, '-')}`;
  const placeholders = detail.placeholders ?? [];

  return (
    <div className="prompt-detail">
      <div className="block">
        <h3>{detail.title}</h3>
        <p className="muted small">
          {detail.description} · <span className="break-all">{detail.prompt_key}</span>
        </p>
        <p className="tag-row">
          <StatusBadge tone="ok">生效：{activeLabel(detail)}</StatusBadge>
          {draft && <StatusBadge tone="info">草稿 v{draft.version}</StatusBadge>}
          {detail.active_version !== 0 && (
            <button type="button" className="button" onClick={() => setPending({ action: 'rollback', version: 0 })}>
              恢复内置默认
            </button>
          )}
        </p>
        <details>
          <summary>查看生效内容（{activeLabel(detail)}）</summary>
          <pre className="prompt-content">{activeContent}</pre>
        </details>
      </div>

      <div className="block form">
        <label htmlFor={editorID}>
          <strong>{draft ? `编辑草稿 v${draft.version}` : '新草稿（从生效内容开始）'}</strong>
        </label>
        <p id={`${editorID}-hint`} className="muted small">
          {placeholders.length > 0 ? `必须且只能包含一次 ${placeholders.join('、')}（运行时替换）。` : ''}共 {[...content].length} 字，最多 20000 字。
        </p>
        <textarea id={editorID} className="prompt-editor" rows={14} value={content} onChange={(e) => setContent(e.target.value)}
          aria-invalid={draftError ? true : undefined} aria-describedby={draftError ? `${editorID}-error` : `${editorID}-hint`} />
        {draftError && (
          <span id={`${editorID}-error`} className="field-error" role="alert">
            {draftError}
          </span>
        )}
        <span className="row-actions">
          <button type="button" className="button" disabled={saving || !dirty || !content.trim()} onClick={saveDraft}>
            保存草稿
          </button>
          {draft && (
            <button type="button" className="button primary" disabled={saving || dirty} onClick={() => setPending({ action: 'publish', version: draft.version })}>
              发布草稿 v{draft.version}
            </button>
          )}
          {draft && dirty && <span className="muted small">有未保存的修改，保存后才能发布</span>}
        </span>
      </div>

      <h3>版本</h3>
      <DataTable
        caption={`${detail.title} 的版本`}
        rows={detail.versions}
        rowKey={(v) => v.prompt_id}
        empty="还没有版本，当前使用内置默认"
        columns={[
          { key: 'version', header: '版本', render: (v) => `v${v.version}` },
          { key: 'status', header: '状态', render: (v) => <StatusBadge tone={statusLabel[v.status][1]}>{statusLabel[v.status][0]}</StatusBadge> },
          { key: 'runes', header: '字数', align: 'end', render: (v) => v.runes },
          { key: 'updated', header: '更新时间', render: (v) => formatDateTime(v.updated_at) },
          { key: 'published', header: '发布时间', render: (v) => (v.published_at ? formatDateTime(v.published_at) : '—') },
          {
            key: 'actions',
            header: '操作',
            render: (v) =>
              v.status === 'archived' ? (
                <button type="button" className="button" aria-label={`回滚到 v${v.version}`} onClick={() => setPending({ action: 'rollback', version: v.version })}>
                  回滚到此版本
                </button>
              ) : (
                '—'
              ),
          },
        ]}
      />

      <h3>发布记录</h3>
      <DataTable
        caption={`${detail.title} 的发布记录`}
        rows={detail.publish_records}
        rowKey={(r) => r.record_id}
        empty="还没有发布记录"
        columns={[
          { key: 'time', header: '时间', render: (r) => formatDateTime(r.created_at) },
          { key: 'action', header: '动作', render: (r) => (r.action === 'publish' ? '发布' : '回滚') },
          { key: 'change', header: '版本', render: (r) => `${versionLabel(r.from_version)} → ${versionLabel(r.version)}` },
          { key: 'by', header: '操作人', render: (r) => r.published_by },
          { key: 'reason', header: '原因', render: (r) => r.reason || '—' },
          { key: 'result', header: '结果', render: (r) => <StatusBadge tone={resultLabel[r.result][1]}>{resultLabel[r.result][0]}</StatusBadge> },
        ]}
      />

      <ReasonDialog
        open={pending !== null}
        title={pending?.action === 'publish' ? `发布 v${pending.version}` : `回滚到${versionLabel(pending?.version ?? 0)}`}
        message={
          pending?.action === 'publish'
            ? `发布后立即替换当前生效的${activeLabel(detail)}，新的导购对话会使用这个版本。`
            : `回滚后立即替换当前生效的${activeLabel(detail)}，改用${versionLabel(pending?.version ?? 0)}的内容。`
        }
        confirmText={pending?.action === 'publish' ? '确认发布' : '确认回滚'}
        requireReason={pending?.action === 'rollback'}
        danger={pending?.action === 'rollback'}
        busy={busy}
        error={switchError}
        onConfirm={confirmSwitch}
        onCancel={cancelSwitch}
      />
    </div>
  );
}
