import { useRef, useState, type FormEvent } from 'react';
import { listMerchants } from '../api/catalog';
import { ApiError } from '../api/http';
import { createMerchantDocument, ingestDocument, type DocumentScope } from '../api/knowledge';
import { listMerchantProducts } from '../api/merchant';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Field } from '../components/FormField';
import { invalid } from '../lib/invalid';
import { notify } from '../lib/notice';
import { docTypeLabel } from '../lib/format';
import { documentHref, documentsHref, navigate } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { DocType, IngestionInput } from '../types/api';

type SourceKind = 'text' | 'html' | 'json' | 'url';

const sourceKinds: { value: SourceKind; label: string; hint: string }[] = [
  { value: 'text', label: '文字资料', hint: '直接粘贴说明文字，空行分段' },
  { value: 'html', label: '网页源码', hint: '粘贴 HTML，脚本、样式和导航会被去掉' },
  { value: 'json', label: 'JSON', hint: '粘贴 JSON，会展开成“字段: 值”的文字' },
  { value: 'url', label: '网页地址', hint: '由服务器抓取公开网页（http/https，不能是内网地址）' },
];

const merchantDocTypes: DocType[] = ['product_detail', 'faq', 'policy', 'guide'];

interface FormState {
  owner: string; // 仅管理员：'' 为平台资料，否则为商家 ID
  kind: SourceKind;
  title: string;
  docType: DocType;
  productId: string;
  body: string;
  force: boolean;
}

const emptyForm: FormState = { owner: '', kind: 'text', title: '', docType: 'product_detail', productId: '', body: '', force: false };

type Errors = Partial<Record<'owner' | 'title' | 'docType' | 'productId' | 'body', string>>;

// 服务端字段名 → 表单字段。
const fieldMap: Record<string, keyof Errors> = {
  merchant_id: 'owner',
  title: 'title',
  doc_type: 'docType',
  product_id: 'productId',
  content: 'body',
  html: 'body',
  json_text: 'body',
  source_url: 'body',
};

function validate(f: FormState, scope: DocumentScope): Errors {
  const e: Errors = {};
  const title = f.title.trim();
  if (scope === 'merchant' && f.kind === 'text' && !title) e.title = '请填写资料标题';
  if ([...title].length > 120) e.title = '标题不能超过 120 个字';
  if (/[\n\r\t]/.test(f.title)) e.title = '标题不能换行';
  const body = f.body.trim();
  if (!body) e.body = f.kind === 'url' ? '请填写网页地址' : '请填写内容';
  else if (f.kind === 'url' && !/^https?:\/\/\S+$/i.test(body)) e.body = '请填写以 http:// 或 https:// 开头的地址';
  return e;
}

export function DocumentFormPage({ scope }: { scope: DocumentScope }) {
  const [form, setForm] = useState<FormState>(emptyForm);
  const [errors, setErrors] = useState<Errors>({});
  const [summary, setSummary] = useState('');
  const [saving, setSaving] = useState(false);
  const [confirmLeave, setConfirmLeave] = useState(false);
  const inFlight = useRef(false);
  const dirty = JSON.stringify(form) !== JSON.stringify(emptyForm);

  const [products] = useRequest(`doc-products|${scope}`, () =>
    scope === 'merchant'
      ? listMerchantProducts({ pageSize: 100 })
      : Promise.resolve({ items: [], page: 1, page_size: 0, total: 0 }),
  );
  const [merchants] = useRequest(`doc-merchants|${scope}`, () =>
    scope === 'admin' ? listMerchants() : Promise.resolve({ items: [], page: 1, page_size: 0, total: 0 }),
  );

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm((f) => ({ ...f, [key]: value }));

  function showErrors(next: Errors, message: string) {
    setErrors(next);
    setSummary(message);
    requestAnimationFrame(() => {
      const first = document.querySelector<HTMLElement>('[aria-invalid="true"]');
      (first ?? document.getElementById('form-summary'))?.focus();
    });
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (inFlight.current) return; // 防止连点重复提交
    const local = validate(form, scope);
    if (Object.keys(local).length > 0) {
      showErrors(local, '请先修正标出的字段');
      return;
    }
    inFlight.current = true;
    setSaving(true);
    setErrors({});
    setSummary('');
    try {
      const title = form.title.trim();
      const productId = scope === 'merchant' ? form.productId : '';
      if (scope === 'merchant' && form.kind === 'text') {
        const doc = await createMerchantDocument({
          title,
          content: form.body,
          doc_type: form.docType,
          product_id: productId || undefined,
          force_reindex: form.force,
        });
        notify('success', `「${doc.title}」已入库，共 ${doc.chunk_count} 个片段`);
        navigate(documentHref(scope, doc.document_id));
        return;
      }
      const input: IngestionInput = { title: title || undefined, product_id: productId || undefined, force_reindex: form.force };
      if (scope === 'admin') input.merchant_id = form.owner;
      if (form.kind === 'text') input.content = form.body;
      if (form.kind === 'html') input.html = form.body;
      if (form.kind === 'json') input.json_text = form.body;
      if (form.kind === 'url') input.source_url = form.body.trim();
      const res = await ingestDocument(scope, input);
      const d = res.document;
      notify('success', 
        res.duplicate
          ? `内容与已有资料「${d.title}」相同，没有重复入库`
          : `「${d.title}」已入库，共 ${d.chunk_count} 个片段${res.truncated ? '（内容过长，已截断）' : ''}`,
      );
      navigate(documentHref(scope, d.document_id));
    } catch (err) {
      if (err instanceof ApiError) {
        const key = err.field ? fieldMap[err.field] : undefined;
        showErrors(key ? { [key]: err.message } : {}, err.message);
      } else {
        showErrors({}, '提交失败，请稍后重试');
      }
    } finally {
      inFlight.current = false;
      setSaving(false);
    }
  }

  function cancel() {
    if (dirty) setConfirmLeave(true);
    else navigate(documentsHref(scope));
  }

  const kind = sourceKinds.find((k) => k.value === form.kind)!;
  return (
    <section aria-labelledby="doc-form-title">
      <h2 id="doc-form-title">{scope === 'admin' ? '采集资料' : '添加资料'}</h2>
      <form className="form doc-form" onSubmit={submit} noValidate>
        {summary && (
          <p id="form-summary" className="form-error" role="alert" tabIndex={-1}>
            {summary}
          </p>
        )}

        {scope === 'admin' && (
          <Field label="归属" id="f-owner" error={errors.owner} hint="平台资料对所有商家的导购都可见">
            <select id="f-owner" value={form.owner} onChange={(e) => set('owner', e.target.value)} {...invalid(errors.owner, 'f-owner')}>
              <option value="">平台资料</option>
              {merchants.status === 'ok' &&
                merchants.data.items.map((m) => (
                  <option key={m.merchant_id} value={m.merchant_id}>
                    {m.name}
                  </option>
                ))}
            </select>
          </Field>
        )}

        <fieldset>
          <legend>来源</legend>
          <div className="choice-row">
            {sourceKinds.map((k) => (
              <label key={k.value} className="choice">
                <input type="radio" name="kind" value={k.value} checked={form.kind === k.value} onChange={() => set('kind', k.value)} />
                {k.label}
              </label>
            ))}
          </div>
        </fieldset>

        <Field
          label="标题"
          id="f-title"
          required={scope === 'merchant' && form.kind === 'text'}
          error={errors.title}
          hint={scope === 'merchant' && form.kind === 'text' ? undefined : '留空时从网页标题或第一行内容生成'}
        >
          <input id="f-title" value={form.title} maxLength={120} onChange={(e) => set('title', e.target.value)} {...invalid(errors.title, 'f-title')} />
        </Field>

        {scope === 'merchant' && form.kind === 'text' && (
          <Field label="资料类型" id="f-type" error={errors.docType} hint="“常见问题”按“问：…/答：…”拆成一问一答">
            <select id="f-type" value={form.docType} onChange={(e) => set('docType', e.target.value as DocType)} {...invalid(errors.docType, 'f-type')}>
              {merchantDocTypes.map((t) => (
                <option key={t} value={t}>
                  {docTypeLabel[t]}
                </option>
              ))}
            </select>
          </Field>
        )}

        {scope === 'merchant' && (
          <Field label="关联商品" id="f-product" error={errors.productId} hint="可选；关联后导购介绍该商品时会优先引用">
            <select id="f-product" value={form.productId} onChange={(e) => set('productId', e.target.value)} {...invalid(errors.productId, 'f-product')}>
              <option value="">不关联</option>
              {products.status === 'ok' &&
                products.data.items.map((p) => (
                  <option key={p.product_id} value={p.product_id}>
                    {p.name}
                  </option>
                ))}
            </select>
          </Field>
        )}

        <Field label={form.kind === 'url' ? '网页地址' : '内容'} id="f-body" required error={errors.body} hint={kind.hint}>
          {form.kind === 'url' ? (
            <input
              id="f-body"
              type="url"
              inputMode="url"
              value={form.body}
              maxLength={1024}
              placeholder="https://"
              onChange={(e) => set('body', e.target.value)}
              {...invalid(errors.body, 'f-body')}
            />
          ) : (
            <textarea id="f-body" rows={12} value={form.body} onChange={(e) => set('body', e.target.value)} {...invalid(errors.body, 'f-body')} />
          )}
        </Field>

        <label className="choice">
          <input type="checkbox" checked={form.force} onChange={(e) => set('force', e.target.checked)} />
          已有相同资料时也重新处理（按这次填写的信息更新）
        </label>

        <div className="form-actions">
          <button type="submit" className="button primary" disabled={saving}>
            {saving ? '提交中…' : '提交'}
          </button>
          <button type="button" className="button" onClick={cancel} disabled={saving}>
            取消
          </button>
        </div>
      </form>

      <ConfirmDialog
        open={confirmLeave}
        title="放弃填写"
        message="填写的内容还没有提交，确定离开吗？"
        confirmText="放弃"
        danger
        onConfirm={() => navigate(documentsHref(scope))}
        onCancel={() => setConfirmLeave(false)}
      />
    </section>
  );
}
