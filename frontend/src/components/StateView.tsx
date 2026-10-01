import type { ReactNode } from 'react';

export function Loading({ text = '加载中…' }: { text?: string }) {
  return (
    <div className="state" role="status" aria-live="polite">
      {text}
    </div>
  );
}

export function ErrorState({ message, onRetry }: { message: string; onRetry?: () => void }) {
  return (
    <div className="state state-error" role="alert">
      <p>{message}</p>
      {onRetry && (
        <button type="button" className="button" onClick={onRetry}>
          重试
        </button>
      )}
    </div>
  );
}

export function Empty({ text, children }: { text: string; children?: ReactNode }) {
  return (
    <div className="state state-empty">
      <p>{text}</p>
      {children}
    </div>
  );
}
