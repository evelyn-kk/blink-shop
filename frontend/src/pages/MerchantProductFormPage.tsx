import { useRef, useState, type FormEvent } from 'react';
import { getCategoryTree } from '../api/catalog';
import { ApiError } from '../api/http';
import { createMerchantProduct, getMerchantProduct, updateMerchantProduct } from '../api/merchant';
import { ConfirmDialog } from '../components/ConfirmDialog';
import { Field } from '../components/FormField';
import { invalid } from '../lib/invalid';
import { ErrorState, Loading } from '../components/StateView';
import { notify } from '../lib/notice';
import { productStatusLabel } from '../lib/format';
import { merchantProductsHref, navigate } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { CategoryNode, MerchantProduct, ProductInput } from '../types/api';

// ---------- 表单状态 ----------

interface SkuRow {
  key: string;
  sku_id?: string;
  sku_name: string;
  price: string;
  stock_quantity: string;
  specs: string; // “颜色=白；存储=128GB”
  is_default: boolean;
}

interface AttrRow {
  key: string;
  name: string;
  value: string;
  unit: string;
}

interface FormState {
  name: string;
  brand: string;
  category_id: string;
  status: 'active' | 'inactive';
  market_price: string;
  image_urls: string;
  tags: string;
  selling_points: string;
  risk_notes: string;
  suitable_for: string;
  not_suitable_for: string;
  recommend_reason: string;
  description: string;
  skus: SkuRow[];
  attributes: AttrRow[];
}

let rowSeq = 0;
const rowKey = () => `row${++rowSeq}`;

const emptySku = (isDefault: boolean): SkuRow => ({
  key: rowKey(),
  sku_name: '',
  price: '',
  stock_quantity: '0',
  specs: '',
  is_default: isDefault,
});

function emptyForm(): FormState {
  return {
    name: '',
    brand: '',
    category_id: '',
    status: 'active',
    market_price: '',
    image_urls: '',
    tags: '',
    selling_points: '',
    risk_notes: '',
    suitable_for: '',
    not_suitable_for: '',
    recommend_reason: '',
    description: '',
    skus: [{ ...emptySku(true), sku_name: '默认款' }],
    attributes: [],
  };
}

const lines = (v: string[]) => v.join('\n');

function fromProduct(p: MerchantProduct): FormState {
  return {
    name: p.name,
    brand: p.brand,
    category_id: p.category_id,
    status: p.status === 'inactive' ? 'inactive' : 'active',
    market_price: p.market_price === '0.00' ? '' : p.market_price,
    image_urls: lines(p.image_urls),
    tags: p.tags.join('，'),
    selling_points: lines(p.selling_points),
    risk_notes: lines(p.risk_notes),
    suitable_for: lines(p.suitable_for),
    not_suitable_for: lines(p.not_suitable_for),
    recommend_reason: p.recommend_reason,
    description: p.description,
    skus: p.skus.map((s) => ({
      key: rowKey(),
      sku_id: s.sku_id,
      sku_name: s.sku_name,
      price: s.price,
      stock_quantity: String(s.stock_quantity),
      specs: Object.entries(s.specs)
        .map(([k, v]) => `${k}=${v}`)
        .join('；'),
      is_default: s.is_default,
    })),
    attributes: p.attributes.map((a) => ({ key: rowKey(), name: a.key, value: a.value, unit: a.unit })),
  };
}

const splitLines = (v: string) =>
  v
    .split('\n')
    .map((s) => s.trim())
    .filter(Boolean);
const splitTags = (v: string) =>
  v
    .split(/[,，、\n]/)
    .map((s) => s.trim())
    .filter(Boolean);

/** 解析“k=v；k2=v2”。格式不对返回 null。 */
function parseSpecs(text: string): Record<string, string> | null {
  const out: Record<string, string> = {};
  for (const part of text.split(/[;；\n]/)) {
    const item = part.trim();
    if (!item) continue;
    const m = /^([^=:：]+)[=:：](.+)$/.exec(item);
    if (!m) return null;
    out[m[1].trim()] = m[2].trim();
  }
  return out;
}

const MONEY = /^\d{1,8}(\.\d{1,2})?$/;

type Errors = Record<string, string>;

/** 客户端先做必填和格式检查，减少无谓请求；服务端仍做完整校验。 */
function validate(f: FormState): Errors {
  const e: Errors = {};
  if (!f.name.trim()) e.name = '请填写商品名称';
  if (!f.category_id) e.category_id = '请选择分类';
  if (f.market_price.trim() && !MONEY.test(f.market_price.trim())) e.market_price = '金额格式不正确，最多两位小数';
  if (f.skus.length === 0) e.skus = '至少需要一个规格';
  f.skus.forEach((s, i) => {
    if (!s.sku_name.trim()) e[`skus[${i}].sku_name`] = '请填写规格名称';
    const price = s.price.trim();
    if (!MONEY.test(price) || Number(price) <= 0) e[`skus[${i}].price`] = '请填写大于 0 的价格，最多两位小数';
    if (!/^\d+$/.test(s.stock_quantity.trim()) || Number(s.stock_quantity) > 1_000_000) {
      e[`skus[${i}].stock_quantity`] = '库存是 0 到 1000000 的整数';
    }
    if (parseSpecs(s.specs) === null) e[`skus[${i}].specs`] = '格式为“属性=值”，多个用分号分隔';
  });
  return e;
}

function toInput(f: FormState, includeStatus: boolean): ProductInput {
  const images = splitLines(f.image_urls);
  const input: ProductInput = {
    image_url: images[0] ?? '', // 第一行为主图
    name: f.name.trim(),
    brand: f.brand.trim(),
    category_id: f.category_id,
    market_price: f.market_price.trim() || '0',
    image_urls: images,
    tags: splitTags(f.tags),
    selling_points: splitLines(f.selling_points),
    risk_notes: splitLines(f.risk_notes),
    suitable_for: splitLines(f.suitable_for),
    not_suitable_for: splitLines(f.not_suitable_for),
    recommend_reason: f.recommend_reason.trim(),
    description: f.description.trim(),
    attributes: f.attributes.map((a) => ({ key: a.name.trim(), value: a.value.trim(), unit: a.unit.trim() })),
    skus: f.skus.map((s) => ({
      ...(s.sku_id ? { sku_id: s.sku_id } : {}),
      sku_name: s.sku_name.trim(),
      price: s.price.trim(),
      stock_quantity: Number(s.stock_quantity),
      specs: parseSpecs(s.specs) ?? {},
      is_default: s.is_default,
    })),
  };
  if (includeStatus) input.status = f.status;
  return input;
}

/** 把服务端的 field（如 tags[2]、attributes[1].key）对应到表单上显示错误的位置。 */
function displayKey(field: string): string {
  const list = /^(tags|selling_points|risk_notes|suitable_for|not_suitable_for|image_urls)\[\d+\]$/.exec(field);
  if (list) return list[1];
  const attr = /^(attributes\[\d+\])(\.\w+)?$/.exec(field);
  if (attr) return attr[1];
  if (field === 'image_url') return 'image_urls';
  if (field === 'price' || field === 'stock_quantity') return 'skus';
  return field;
}

// ---------- 页面 ----------

export function MerchantProductFormPage({ id }: { id?: string }) {
  const [categories] = useRequest('categories', getCategoryTree);
  const [product, reload] = useRequest(`merchant-product|${id ?? 'new'}`, () =>
    id ? getMerchantProduct(id) : Promise.resolve(null),
  );

  if (categories.status === 'loading' || product.status === 'loading') return <Loading />;
  if (product.status === 'error') return <ErrorState message={product.message} onRetry={reload} />;
  if (categories.status === 'error') return <ErrorState message={categories.message} />;
  return <ProductForm key={id ?? 'new'} product={product.data} categories={categories.data} />;
}

function ProductForm({ product, categories }: { product: MerchantProduct | null; categories: CategoryNode[] }) {
  const [initial] = useState(() => (product ? fromProduct(product) : emptyForm()));
  const [form, setForm] = useState<FormState>(initial);
  const [errors, setErrors] = useState<Errors>({});
  const [summary, setSummary] = useState('');
  const [saving, setSaving] = useState(false);
  const [confirmLeave, setConfirmLeave] = useState(false);
  const inFlight = useRef(false);
  const underReview = product?.status === 'risk';
  const dirty = JSON.stringify(form) !== JSON.stringify(initial);

  const set = <K extends keyof FormState>(key: K, value: FormState[K]) => setForm((f) => ({ ...f, [key]: value }));
  const setSku = (i: number, patch: Partial<SkuRow>) =>
    setForm((f) => ({ ...f, skus: f.skus.map((s, j) => (j === i ? { ...s, ...patch } : s)) }));
  const setDefaultSku = (i: number) =>
    setForm((f) => ({ ...f, skus: f.skus.map((s, j) => ({ ...s, is_default: j === i })) }));
  const removeSku = (i: number) =>
    setForm((f) => {
      const skus = f.skus.filter((_, j) => j !== i);
      if (skus.length > 0 && !skus.some((s) => s.is_default)) skus[0] = { ...skus[0], is_default: true };
      return { ...f, skus };
    });
  const setAttr = (i: number, patch: Partial<AttrRow>) =>
    setForm((f) => ({ ...f, attributes: f.attributes.map((a, j) => (j === i ? { ...a, ...patch } : a)) }));

  function showErrors(next: Errors, message: string) {
    setErrors(next);
    setSummary(message);
    // 焦点移到第一个出错的输入框，屏幕阅读器和键盘用户可以直接修改。
    requestAnimationFrame(() => {
      const first = document.querySelector<HTMLElement>('[aria-invalid="true"]');
      (first ?? document.getElementById('form-summary'))?.focus();
    });
  }

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (inFlight.current) return; // 防止连点重复提交
    const local = validate(form);
    if (Object.keys(local).length > 0) {
      showErrors(local, '请先修正标出的字段');
      return;
    }
    inFlight.current = true;
    setSaving(true);
    setErrors({});
    setSummary('');
    try {
      const input = toInput(form, !underReview);
      const saved = product
        ? await updateMerchantProduct(product.product_id, input)
        : await createMerchantProduct(input);
      notify('success', product ? `已保存「${saved.name}」` : `已创建「${saved.name}」`);
      navigate(merchantProductsHref());
    } catch (err) {
      if (err instanceof ApiError) {
        showErrors(err.field ? { [displayKey(err.field)]: err.message } : {}, err.message);
      } else {
        showErrors({}, '保存失败，请稍后重试');
      }
    } finally {
      inFlight.current = false;
      setSaving(false);
    }
  }

  function cancel() {
    if (dirty) setConfirmLeave(true);
    else navigate(merchantProductsHref());
  }

  const err = (key: string) => errors[key];

  return (
    <section aria-labelledby="form-title">
      <h2 id="form-title">{product ? '编辑商品' : '新建商品'}</h2>
      <form className="form product-form" onSubmit={submit} noValidate>
        {summary && (
          <p id="form-summary" className="form-error" role="alert" tabIndex={-1}>
            {summary}
          </p>
        )}

        <fieldset>
          <legend>基本信息</legend>
          <Field label="商品名称" required error={err('name')} id="f-name">
            <input
              id="f-name"
              value={form.name}
              maxLength={128}
              onChange={(e) => set('name', e.target.value)}
              {...invalid(err('name'), 'f-name')}
            />
          </Field>
          <div className="grid-2">
            <Field label="品牌" error={err('brand')} id="f-brand">
              <input id="f-brand" value={form.brand} maxLength={64} onChange={(e) => set('brand', e.target.value)} />
            </Field>
            <Field label="分类" required error={err('category_id')} id="f-category">
              <select
                id="f-category"
                value={form.category_id}
                onChange={(e) => set('category_id', e.target.value)}
                {...invalid(err('category_id'), 'f-category')}
              >
                <option value="">请选择</option>
                {categories.map((root) => (
                  <optgroup key={root.category_id} label={root.name}>
                    <option value={root.category_id}>{root.name}（全部）</option>
                    {root.children.map((c) => (
                      <option key={c.category_id} value={c.category_id}>
                        {c.name}
                      </option>
                    ))}
                  </optgroup>
                ))}
              </select>
            </Field>
          </div>
          <div className="grid-2">
            <Field label="状态" id="f-status" error={err('status')} hint={underReview ? '风控审核中，不能自行上下架' : undefined}>
              {underReview ? (
                <input id="f-status" value={productStatusLabel.risk} disabled />
              ) : (
                <select id="f-status" value={form.status} onChange={(e) => set('status', e.target.value as FormState['status'])}>
                  <option value="active">上架</option>
                  <option value="inactive">下架</option>
                </select>
              )}
            </Field>
            <Field label="市场价（划线价）" id="f-market" error={err('market_price')} hint="留空则不显示划线价；不能低于售价">
              <input
                id="f-market"
                inputMode="decimal"
                value={form.market_price}
                onChange={(e) => set('market_price', e.target.value)}
                {...invalid(err('market_price'), 'f-market')}
              />
            </Field>
          </div>
        </fieldset>

        <fieldset>
          <legend>
            规格<span className="required">*</span>
          </legend>
          <p className="muted small">售价取默认规格的价格，总库存为各规格之和。</p>
          {err('skus') && (
            <p className="field-error" role="alert">
              {err('skus')}
            </p>
          )}
          <div className="sku-list">
            {form.skus.map((s, i) => (
              <div key={s.key} className="sku-row" role="group" aria-label={`规格 ${i + 1}`}>
                <Field label="规格名称" required id={`f-sku-${i}-name`} error={err(`skus[${i}].sku_name`)}>
                  <input
                    id={`f-sku-${i}-name`}
                    value={s.sku_name}
                    maxLength={64}
                    onChange={(e) => setSku(i, { sku_name: e.target.value })}
                    {...invalid(err(`skus[${i}].sku_name`), `f-sku-${i}-name`)}
                  />
                </Field>
                <Field label="价格（元）" required id={`f-sku-${i}-price`} error={err(`skus[${i}].price`)}>
                  <input
                    id={`f-sku-${i}-price`}
                    inputMode="decimal"
                    value={s.price}
                    onChange={(e) => setSku(i, { price: e.target.value })}
                    {...invalid(err(`skus[${i}].price`), `f-sku-${i}-price`)}
                  />
                </Field>
                <Field label="库存" required id={`f-sku-${i}-stock`} error={err(`skus[${i}].stock_quantity`)}>
                  <input
                    id={`f-sku-${i}-stock`}
                    inputMode="numeric"
                    value={s.stock_quantity}
                    onChange={(e) => setSku(i, { stock_quantity: e.target.value })}
                    {...invalid(err(`skus[${i}].stock_quantity`), `f-sku-${i}-stock`)}
                  />
                </Field>
                <Field label="属性" id={`f-sku-${i}-specs`} error={err(`skus[${i}].specs`)} hint="如：颜色=白；存储=128GB">
                  <input
                    id={`f-sku-${i}-specs`}
                    value={s.specs}
                    onChange={(e) => setSku(i, { specs: e.target.value })}
                    {...invalid(err(`skus[${i}].specs`), `f-sku-${i}-specs`)}
                  />
                </Field>
                <div className="sku-row-actions">
                  <label className="inline">
                    <input type="radio" name="default-sku" checked={s.is_default} onChange={() => setDefaultSku(i)} />
                    默认规格
                  </label>
                  <button
                    type="button"
                    className="button"
                    onClick={() => removeSku(i)}
                    disabled={form.skus.length === 1}
                    aria-label={`删除规格 ${i + 1}`}
                  >
                    删除规格
                  </button>
                </div>
              </div>
            ))}
          </div>
          <button
            type="button"
            className="button"
            onClick={() => set('skus', [...form.skus, emptySku(false)])}
            disabled={form.skus.length >= 20}
          >
            添加规格
          </button>
        </fieldset>

        <fieldset>
          <legend>图片与卖点</legend>
          <Field label="图片地址" id="f-images" error={err('image_urls')} hint="每行一个，第一张为主图；支持 https 链接或平台内图片，最多 9 张">
            <textarea
              id="f-images"
              rows={3}
              value={form.image_urls}
              onChange={(e) => set('image_urls', e.target.value)}
              {...invalid(err('image_urls'), 'f-images')}
            />
          </Field>
          <Field label="标签" id="f-tags" error={err('tags')} hint="用逗号分隔，最多 10 个">
            <input id="f-tags" value={form.tags} onChange={(e) => set('tags', e.target.value)} {...invalid(err('tags'), 'f-tags')} />
          </Field>
          <div className="grid-2">
            <ListField label="卖点" id="f-points" value={form.selling_points} error={err('selling_points')} onChange={(v) => set('selling_points', v)} />
            <ListField label="风险提示" id="f-risks" value={form.risk_notes} error={err('risk_notes')} onChange={(v) => set('risk_notes', v)} />
            <ListField label="适合" id="f-suitable" value={form.suitable_for} error={err('suitable_for')} onChange={(v) => set('suitable_for', v)} />
            <ListField label="不适合" id="f-unsuitable" value={form.not_suitable_for} error={err('not_suitable_for')} onChange={(v) => set('not_suitable_for', v)} />
          </div>
        </fieldset>

        <fieldset>
          <legend>参数</legend>
          {form.attributes.map((a, i) => (
            <div key={a.key} className="attr-row" role="group" aria-label={`参数 ${i + 1}`}>
              <Field label="参数名" id={`f-attr-${i}-name`} error={err(`attributes[${i}]`)}>
                <input id={`f-attr-${i}-name`} value={a.name} maxLength={16} onChange={(e) => setAttr(i, { name: e.target.value })} {...invalid(err(`attributes[${i}]`), `f-attr-${i}-name`)} />
              </Field>
              <Field label="参数值" id={`f-attr-${i}-value`}>
                <input id={`f-attr-${i}-value`} value={a.value} maxLength={64} onChange={(e) => setAttr(i, { value: e.target.value })} />
              </Field>
              <Field label="单位" id={`f-attr-${i}-unit`}>
                <input id={`f-attr-${i}-unit`} value={a.unit} maxLength={8} onChange={(e) => setAttr(i, { unit: e.target.value })} />
              </Field>
              <button
                type="button"
                className="button"
                aria-label={`删除参数 ${i + 1}`}
                onClick={() => set('attributes', form.attributes.filter((_, j) => j !== i))}
              >
                删除
              </button>
            </div>
          ))}
          <button
            type="button"
            className="button"
            onClick={() => set('attributes', [...form.attributes, { key: rowKey(), name: '', value: '', unit: '' }])}
            disabled={form.attributes.length >= 30}
          >
            添加参数
          </button>
        </fieldset>

        <fieldset>
          <legend>介绍</legend>
          <Field label="推荐理由" id="f-reason" error={err('recommend_reason')}>
            <input id="f-reason" value={form.recommend_reason} maxLength={200} onChange={(e) => set('recommend_reason', e.target.value)} />
          </Field>
          <Field label="商品介绍" id="f-desc" error={err('description')}>
            <textarea id="f-desc" rows={5} value={form.description} maxLength={5000} onChange={(e) => set('description', e.target.value)} />
          </Field>
        </fieldset>

        <div className="form-actions">
          <button type="button" className="button" onClick={cancel} disabled={saving}>
            取消
          </button>
          <button type="submit" className="button primary" disabled={saving}>
            {saving ? '保存中…' : product ? '保存修改' : '创建商品'}
          </button>
        </div>
      </form>

      <ConfirmDialog
        open={confirmLeave}
        title="放弃修改"
        message="有尚未保存的修改，确定离开吗？"
        confirmText="放弃修改"
        danger
        onConfirm={() => navigate(merchantProductsHref())}
        onCancel={() => setConfirmLeave(false)}
      />
    </section>
  );
}

function ListField(props: { label: string; id: string; value: string; error?: string; onChange: (v: string) => void }) {
  return (
    <Field label={props.label} id={props.id} error={props.error} hint="每行一条，最多 10 条">
      <textarea id={props.id} rows={3} value={props.value} onChange={(e) => props.onChange(e.target.value)} {...invalid(props.error, props.id)} />
    </Field>
  );
}
