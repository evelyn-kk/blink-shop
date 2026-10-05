import { dismiss, useNotices } from '../lib/notice';

// 页面顶部的通知区。成功/提示用 role=status（礼貌播报），错误用 role=alert。颜色之外都有文字前缀，不只靠颜色区分。
export function NoticeHost() {
  const notices = useNotices();
  if (notices.length === 0) return null;
  return (
    <div className="toast-host">
      {notices.map((n) => (
        <div key={n.id} className={`toast toast-${n.kind}${n.kind === 'error' ? '' : ' flash'}`} role={n.kind === 'error' ? 'alert' : 'status'}>
          <span className="toast-label">{n.kind === 'error' ? '出错了' : n.kind === 'success' ? '完成' : '提示'}</span>
          <span className="toast-message">{n.message}</span>
          <button type="button" className="toast-close" aria-label="关闭提示" onClick={() => dismiss(n.id)}>
            ×
          </button>
        </div>
      ))}
    </div>
  );
}
