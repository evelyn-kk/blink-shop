import type { StockStatus } from '../types/api';
import { stockLabel } from '../lib/format';

// 状态同时用文字和颜色表达，不只依赖颜色。
export function StockBadge({ status }: { status: StockStatus }) {
  return <span className={`badge badge-${status}`}>{stockLabel[status]}</span>;
}
