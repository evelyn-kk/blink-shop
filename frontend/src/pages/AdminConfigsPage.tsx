import { useState } from 'react';
import { listConfigs, updateConfig } from '../api/admin';
import { ApiError } from '../api/http';
import { ErrorState, Loading } from '../components/StateView';
import { StatusBadge } from '../components/StatusBadge';
import { notify } from '../lib/notice';
import { useRequest } from '../lib/useRequest';
import type { ConfigItem } from '../types/api';

const sourceLabel: Record<ConfigItem['source'], string> = { env: '环境变量', dynamic: '后台设置', default: '默认值' };

// 应用配置：全部配置项的当前值和来源。密钥只显示掩码；运行中可调的配置可以修改（立即生效、写审计），其余只读并说明原因。
export function AdminConfigsPage() {
  const [result, reload] = useRequest('admin-configs', listConfigs);
  return (
    <section aria-labelledby="configs-title">
      <div className="page-head">
        <h2 id="configs-title">应用配置</h2>
        <button type="button" className="button" onClick={reload}>
          刷新
        </button>
      </div>
      <p className="muted small">优先级：环境变量 → 后台设置 → 默认值。后台设置目前保存在内存里，服务重启后恢复。</p>
      {result.status === 'loading' && <Loading />}
      {result.status === 'error' && <ErrorState message={result.message} onRetry={reload} />}
      {result.status === 'ok' && (
        <ul className="doc-list">
          {result.data.items.map((c) => (
            <li key={`${c.key}|${c.value}|${c.source}`} className="doc-row">
              <ConfigRow item={c} onSaved={reload} />
            </li>
          ))}
        </ul>
      )}
    </section>
  );
}

function ConfigRow({ item, onSaved }: { item: ConfigItem; onSaved: () => void }) {
  const [value, setValue] = useState(item.value);
  const [error, setError] = useState('');
  const [saving, setSaving] = useState(false);
  const id = `config-${item.key.replace(/\W/g, '-')}`;

  async function save(next: string) {
    if (saving) return;
    setSaving(true);
    setError('');
    try {
      await updateConfig(item.key, next);
      notify('success', next ? `已保存 ${item.key}` : `${item.key} 已恢复默认值`);
      onSaved();
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '保存失败，请稍后重试');
    } finally {
      setSaving(false);
    }
  }

  const readOnlyReason = item.secret
    ? '密钥不在后台显示或修改'
    : item.source === 'env'
      ? '由环境变量指定，后台修改不会生效'
      : '只在服务启动时读取';

  return (
    <div className="config-row">
      <div>
        <label htmlFor={id}>
          <strong className="break-all">{item.key}</strong>
        </label>
        <p className="muted small">
          {item.description} · 环境变量 {item.env}
        </p>
        <p className="tag-row">
          <StatusBadge tone={item.source === 'dynamic' ? 'info' : 'muted'}>{sourceLabel[item.source]}</StatusBadge>
          {item.secret && <StatusBadge tone="warn">密钥</StatusBadge>}
          {!item.editable && <span className="muted small">只读：{readOnlyReason}</span>}
        </p>
      </div>
      <div className="config-edit">
        <input id={id} value={value} readOnly={!item.editable} onChange={(e) => setValue(e.target.value)} aria-invalid={error ? true : undefined}
          aria-describedby={error ? `${id}-error` : undefined} />
        {error && (
          <span id={`${id}-error`} className="field-error" role="alert">
            {error}
          </span>
        )}
        {item.editable && (
          <span className="row-actions">
            <button type="button" className="button primary" disabled={saving || value.trim() === item.value} onClick={() => save(value.trim())}>
              保存
            </button>
            {item.source === 'dynamic' && (
              <button type="button" className="button" disabled={saving} onClick={() => save('')}>
                恢复默认
              </button>
            )}
          </span>
        )}
      </div>
    </div>
  );
}
