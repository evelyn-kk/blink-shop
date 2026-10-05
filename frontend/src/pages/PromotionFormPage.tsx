import { useRef, useState, type FormEvent } from 'react';
import { getCategoryTree } from '../api/catalog';
import { ApiError } from '../api/http';
import { createPromotion, getPromotion, updatePromotion } from '../api/marketing';
import { listMerchantProducts } from '../api/merchant';
import { Field } from '../components/FormField';
import { ErrorState, Loading } from '../components/StateView';
import { fromLocalInput, rateToZhe, toLocalInput, zheToRate } from '../lib/datetime';
import { invalid } from '../lib/invalid';
import { notify } from '../lib/notice';
import { navigate, promotionsHref } from '../lib/router';
import { useRequest } from '../lib/useRequest';
import type { CategoryNode, MerchantProduct, MerchantPromotion, PromotionInput } from '../types/api';

type Scope = 'merchant' | 'product' | 'category';
type Kind = 'full_reduction' | 'discount';

interface FormState {
  name: string;
  scope: Scope;
  product_id: string;
  category_id: string;
  type: Kind;
  threshold: string;
  amount: string;
  zhe: string;
  stackable: boolean;
  start: string;
  end: string;
  status: 'active' | 'inactive';
}

type Errors = Partial<Record<keyof FormState, string>>;

const MONEY = /^\d{1,8}(\.\d{1,2})?$/;

function initial(p?: MerchantPromotion): FormState {
  if (!p) {
    return { name: '', scope: 'merchant', product_id: '', category_id: '', type: 'full_reduction', threshold: '', amount: '', zhe: '',
      stackable: true, start: '', end: '', status: 'active' };
  }
  return {
    name: p.name, scope: p.scope === 'platform' ? 'merchant' : p.scope, product_id: p.product_id, category_id: p.category_id, type: p.type,
    threshold: Number(p.threshold_amount) > 0 ? String(Number(p.threshold_amount)) : '',
    amount: p.type === 'full_reduction' ? String(Number(p.discount_amount)) : '', zhe: p.type === 'discount' ? rateToZhe(p.discount_rate) : '',
    stackable: p.stackable, start: toLocalInput(p.start_at), end: toLocalInput(p.end_at), status: p.status,
  };
}

function validate(f: FormState, creating: boolean): Errors {
  const e: Errors = {};
  if (!f.name.trim()) e.name = '请填写促销名称';
  if (f.scope === 'product' && !f.product_id) e.product_id = '请选择商品';
  if (f.scope === 'category' && !f.category_id) e.category_id = '请选择分类';
  if (f.threshold && !MONEY.test(f.threshold)) e.threshold = '金额最多两位小数';
  if (f.type === 'full_reduction') {
    if (!MONEY.test(f.amount) || Number(f.amount) <= 0) e.amount = '请填写大于 0 的减免金额';
    else if (f.threshold && Number(f.amount) > Number(f.threshold)) e.amount = '减免金额不能超过门槛';
  } else if (!zheToRate(f.zhe)) {
    e.zhe = '请填写 0 到 10 之间的折数，例如 9.5';
  }
  if (f.start && f.end && fromLocalInput(f.end)! <= fromLocalInput(f.start)!) e.end = '结束时间必须晚于开始时间';
  if (creating && f.end && Date.parse(fromLocalInput(f.end)!) <= Date.now()) e.end = '结束时间必须晚于现在';
  return e;
}

function toInput(f: FormState): PromotionInput {
  const input: PromotionInput = {
    name: f.name.trim(), scope: f.scope, type: f.type, threshold_amount: f.threshold || '0', stackable: f.stackable, status: f.status,
    start_at: fromLocalInput(f.start), end_at: fromLocalInput(f.end),
  };
  if (f.scope === 'product') input.product_id = f.product_id;
  if (f.scope === 'category') input.category_id = f.category_id;
  if (f.type === 'full_reduction') input.discount_amount = f.amount;
  else input.discount_rate = zheToRate(f.zhe);
  return input;
}

// 服务端字段名 → 表单字段。
const serverField: Record<string, keyof FormState> = {
  name: 'name', scope: 'scope', product_id: 'product_id', category_id: 'category_id', type: 'type', threshold_amount: 'threshold',
  discount_amount: 'amount', discount_rate: 'zhe', start_at: 'start', end_at: 'end', status: 'status',
};

function flatten(nodes: CategoryNode[], depth = 0): { id: string; label: string }[] {
  return nodes.flatMap((n) => [{ id: n.category_id, label: `${'　'.repeat(depth)}${n.name}` }, ...flatten(n.children, depth + 1)]);
}

export function PromotionFormPage({ id }: { id?: string }) {
  const [promotion] = useRequest(`promotion|${id ?? 'new'}`, () => (id ? getPromotion(id) : Promise.resolve(undefined)));
  const [products] = useRequest('promotion-products', () => listMerchantProducts({ pageSize: 100 }));
  const [categories] = useRequest('categories', getCategoryTree);
  if (promotion.status === 'loading' || products.status === 'loading' || categories.status === 'loading') return <Loading />;
  for (const r of [promotion, products, categories]) if (r.status === 'error') return <ErrorState message={r.message} />;
  if (promotion.status !== 'ok' || products.status !== 'ok' || categories.status !== 'ok') return null;
  return <PromotionForm key={id ?? 'new'} promotion={promotion.data} products={products.data.items} categories={flatten(categories.data)} />;
}

function PromotionForm(props: { promotion?: MerchantPromotion; products: MerchantProduct[]; categories: { id: string; label: string }[] }) {
  const { promotion } = props;
  const [form, setForm] = useState(() => initial(promotion));
  const [errors, setErrors] = useState<Errors>({});
  const [summary, setSummary] = useState('');
  const [saving, setSaving] = useState(false);
  const inFlight = useRef(false);
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
    const local = validate(form, !promotion);
    if (Object.keys(local).length > 0) {
      showErrors(local, '请先修正标出的字段');
      return;
    }
    inFlight.current = true;
    setSaving(true);
    setErrors({});
    setSummary('');
    try {
      const saved = promotion ? await updatePromotion(promotion.promotion_id, toInput(form)) : await createPromotion(toInput(form));
      notify('success', promotion ? `已保存「${saved.name}」` : `已创建「${saved.name}」`);
      navigate(promotionsHref());
    } catch (err) {
      if (err instanceof ApiError) {
        const key = err.field ? serverField[err.field] : undefined;
        showErrors(key ? { [key]: err.message } : {}, err.message);
      } else {
        showErrors({}, '保存失败，请稍后重试');
      }
    } finally {
      inFlight.current = false;
      setSaving(false);
    }
  }

  const err = (k: keyof FormState) => errors[k];
  const selectable = props.products.filter((p) => p.status !== 'risk' || p.product_id === form.product_id);

  return (
    <section aria-labelledby="promotion-form-title">
      <h2 id="promotion-form-title">{promotion ? '编辑促销' : '新建促销'}</h2>
      <form className="form" onSubmit={submit} noValidate>
        {summary && (
          <p id="form-summary" className="form-error" role="alert" tabIndex={-1}>
            {summary}
          </p>
        )}
        <Field label="名称" id="promo-name" required error={err('name')}>
          <input id="promo-name" value={form.name} maxLength={64} onChange={(e) => set('name', e.target.value)} {...invalid(err('name'), 'promo-name')} />
        </Field>

        <fieldset className="field">
          <legend>范围</legend>
          <div className="choice-row">
            {(
              [
                ['merchant', '全店'],
                ['product', '单品'],
                ['category', '品类'],
              ] as const
            ).map(([value, label]) => (
              <label key={value} className="choice">
                <input type="radio" name="scope" checked={form.scope === value} onChange={() => set('scope', value)} />
                {label}
              </label>
            ))}
          </div>
        </fieldset>
        {form.scope === 'product' && (
          <Field label="商品" id="promo-product" required error={err('product_id')} hint="只能选本店的商品">
            <select id="promo-product" value={form.product_id} onChange={(e) => set('product_id', e.target.value)} {...invalid(err('product_id'), 'promo-product')}>
              <option value="">请选择</option>
              {selectable.map((p) => (
                <option key={p.product_id} value={p.product_id}>
                  {p.name}
                </option>
              ))}
            </select>
          </Field>
        )}
        {form.scope === 'category' && (
          <Field label="分类" id="promo-category" required error={err('category_id')} hint="只作用于本店这个分类（含子分类）下的商品">
            <select id="promo-category" value={form.category_id} onChange={(e) => set('category_id', e.target.value)} {...invalid(err('category_id'), 'promo-category')}>
              <option value="">请选择</option>
              {props.categories.map((c) => (
                <option key={c.id} value={c.id}>
                  {c.label}
                </option>
              ))}
            </select>
          </Field>
        )}

        <fieldset className="field">
          <legend>类型</legend>
          <div className="choice-row">
            <label className="choice">
              <input type="radio" name="type" checked={form.type === 'full_reduction'} onChange={() => set('type', 'full_reduction')} />
              满减
            </label>
            <label className="choice">
              <input type="radio" name="type" checked={form.type === 'discount'} onChange={() => set('type', 'discount')} />
              折扣
            </label>
          </div>
        </fieldset>
        <Field label="门槛（元）" id="promo-threshold" error={err('threshold')} hint="满多少元才生效；不填表示没有门槛">
          <input id="promo-threshold" inputMode="decimal" value={form.threshold} onChange={(e) => set('threshold', e.target.value.trim())} {...invalid(err('threshold'), 'promo-threshold')} />
        </Field>
        {form.type === 'full_reduction' ? (
          <Field label="减免金额（元）" id="promo-amount" required error={err('amount')}>
            <input id="promo-amount" inputMode="decimal" value={form.amount} onChange={(e) => set('amount', e.target.value.trim())} {...invalid(err('amount'), 'promo-amount')} />
          </Field>
        ) : (
          <Field label="折数" id="promo-zhe" required error={err('zhe')} hint="例如 9.5 表示 9.5 折（按原价的 95% 付款）">
            <input id="promo-zhe" inputMode="decimal" value={form.zhe} onChange={(e) => set('zhe', e.target.value.trim())} {...invalid(err('zhe'), 'promo-zhe')} />
          </Field>
        )}
        <label className="choice">
          <input type="checkbox" checked={form.stackable} onChange={(e) => set('stackable', e.target.checked)} />
          可以与其他促销和优惠券叠加
        </label>

        <Field label="开始时间" id="promo-start" error={err('start')} hint={promotion ? undefined : '不填为现在'}>
          <input id="promo-start" type="datetime-local" value={form.start} onChange={(e) => set('start', e.target.value)} {...invalid(err('start'), 'promo-start')} />
        </Field>
        <Field label="结束时间" id="promo-end" error={err('end')} hint={promotion ? '最长 366 天' : '不填为开始后 30 天；最长 366 天'}>
          <input id="promo-end" type="datetime-local" value={form.end} onChange={(e) => set('end', e.target.value)} {...invalid(err('end'), 'promo-end')} />
        </Field>
        <Field label="状态" id="promo-status" error={err('status')}>
          <select id="promo-status" value={form.status} onChange={(e) => set('status', e.target.value as FormState['status'])}>
            <option value="active">启用</option>
            <option value="inactive">停用</option>
          </select>
        </Field>

        <div className="form-actions">
          <a className="button" href={promotionsHref()}>
            取消
          </a>
          <button type="submit" className="button primary" disabled={saving} aria-busy={saving}>
            {saving ? '保存中…' : promotion ? '保存修改' : '创建促销'}
          </button>
        </div>
      </form>
    </section>
  );
}
