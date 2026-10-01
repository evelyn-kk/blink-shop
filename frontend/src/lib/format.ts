import type { Money, Promotion, StockStatus } from '../types/api';

const money = new Intl.NumberFormat('zh-CN', { style: 'currency', currency: 'CNY' });

// 金额只在展示时转成数字；计算一律由服务端完成。
export function formatMoney(value: Money): string {
  const n = Number(value);
  return Number.isFinite(n) ? money.format(n) : value;
}

export function hasDiscount(price: Money, marketPrice: Money): boolean {
  return Number(marketPrice) > Number(price);
}

export const stockLabel: Record<StockStatus, string> = {
  in_stock: '有货',
  low_stock: '库存紧张',
  out_of_stock: '暂时无货',
};

export function formatDate(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return iso;
  return d.toLocaleDateString('zh-CN', { year: 'numeric', month: '2-digit', day: '2-digit' });
}

/** 促销的简短说明，例如“满 300 减 30”“9.5 折”。 */
export function describePromotion(p: Promotion): string {
  if (p.type === 'discount') {
    const zhe = Number(p.discount_rate) * 10;
    return `${Number(zhe.toFixed(2))} 折`;
  }
  return `满 ${Number(p.threshold_amount)} 减 ${Number(p.discount_amount)}`;
}

export const scopeLabel: Record<Promotion['scope'], string> = {
  platform: '平台',
  merchant: '店铺',
  product: '单品',
  category: '品类',
};
