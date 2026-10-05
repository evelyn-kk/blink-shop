import { describe, expect, it } from 'vitest';
import type { MerchantPromotion } from '../types/api';
import { fromLocalInput, rateToZhe, toLocalInput, zheToRate } from './datetime';
import { promotionState } from './promotion';

describe('折数与折扣率', () => {
  it.each([
    ['9.5', '0.9500'],
    ['9', '0.9000'],
    ['8.85', '0.8850'],
    ['0.5', '0.0500'],
  ])('%s 折 → %s', (zhe, rate) => {
    expect(zheToRate(zhe)).toBe(rate);
    expect(rateToZhe(rate)).toBe(String(Number(zhe)));
  });
  it.each(['', '0', '10', '10.5', '-1', '9.555', 'abc', '9.'])('非法折数 %j', (zhe) => expect(zheToRate(zhe)).toBeUndefined());
});

describe('本地时间输入', () => {
  it('来回转换不丢失分钟', () => {
    const iso = '2026-10-01T08:30:00Z';
    expect(fromLocalInput(toLocalInput(iso))).toBe(iso);
  });
  it('空值和非法值', () => {
    expect(fromLocalInput('')).toBeUndefined();
    expect(fromLocalInput('not a time')).toBeUndefined();
    expect(toLocalInput('bad')).toBe('');
  });
});

describe('促销实际状态', () => {
  const p = (status: 'active' | 'inactive', start: string, end: string) =>
    ({ status, start_at: start, end_at: end }) as MerchantPromotion;
  const now = Date.parse('2026-10-05T00:00:00Z');
  it.each([
    [p('inactive', '2026-10-01T00:00:00Z', '2026-10-10T00:00:00Z'), '已停用'],
    [p('active', '2026-10-06T00:00:00Z', '2026-10-10T00:00:00Z'), '未开始'],
    [p('active', '2026-10-01T00:00:00Z', '2026-10-05T00:00:00Z'), '已结束'],
    [p('active', '2026-10-01T00:00:00Z', '2026-10-10T00:00:00Z'), '进行中'],
  ])('%#', (promo, label) => expect(promotionState(promo, now).label).toBe(label));
});
