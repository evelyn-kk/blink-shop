import type { BadgeTone } from '../components/StatusBadge';

export const statusLabel: Record<string, string> = {
  active: '正常',
  inactive: '停用',
  risk: '风控',
  deleted: '已删除',
  visible: '显示',
  hidden: '已隐藏',
};

export const statusTone: Record<string, BadgeTone> = {
  active: 'ok',
  inactive: 'muted',
  risk: 'bad',
  deleted: 'muted',
  visible: 'ok',
  hidden: 'warn',
};

/** 恢复到这些状态不需要写原因；其余（停用、风控、隐藏）必须写。 */
export function needsReason(status: string): boolean {
  return status !== 'active' && status !== 'visible';
}
