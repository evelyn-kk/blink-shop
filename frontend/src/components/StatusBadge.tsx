import type { ReactNode } from 'react';

export type BadgeTone = 'ok' | 'warn' | 'bad' | 'info' | 'muted';

// 状态标签：文字本身就是状态，颜色只是辅助（不能只靠颜色区分）。
export function StatusBadge({ tone, children, className = '' }: { tone: BadgeTone; children: ReactNode; className?: string }) {
  return <span className={`status tone-${tone} ${className}`.trim()}>{children}</span>;
}
