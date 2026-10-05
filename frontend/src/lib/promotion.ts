import type { BadgeTone } from '../components/StatusBadge';
import type { MerchantPromotion } from '../types/api';

/** 促销当前的实际状态：停用的显示“已停用”，启用的按时间分为未开始 / 进行中 / 已结束。 */
export function promotionState(p: MerchantPromotion, now = Date.now()): { label: string; tone: BadgeTone } {
  if (p.status === 'inactive') return { label: '已停用', tone: 'muted' };
  if (now < Date.parse(p.start_at)) return { label: '未开始', tone: 'info' };
  if (now >= Date.parse(p.end_at)) return { label: '已结束', tone: 'muted' };
  return { label: '进行中', tone: 'ok' };
}
