import { useEffect, useRef, useState } from 'react';

// 敏感操作的确认对话框：说明后果，需要时填写原因（记入操作审计）。原生模态 <dialog>，Esc 等同取消，焦点默认在“取消”。
export function ReasonDialog(props: {
  open: boolean;
  title: string;
  message: string;
  confirmText: string;
  requireReason: boolean;
  danger?: boolean;
  busy?: boolean;
  error?: string;
  onConfirm: (reason: string) => void;
  onCancel: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const [reason, setReason] = useState('');
  const [missing, setMissing] = useState(false);
  const { open } = props;

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  function confirm() {
    const text = reason.trim();
    if (props.requireReason && !text) {
      setMissing(true);
      return;
    }
    props.onConfirm(text);
  }

  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby="reason-title"
      onCancel={(e) => {
        e.preventDefault();
        props.onCancel();
      }}
      onClose={() => {
        setReason('');
        setMissing(false);
      }}
    >
      <h3 id="reason-title">{props.title}</h3>
      <p>{props.message}</p>
      {props.requireReason && (
        <div className="field">
          <label htmlFor="reason-input">
            原因<span className="required">*</span>
          </label>
          <textarea
            id="reason-input"
            rows={3}
            maxLength={200}
            value={reason}
            onChange={(e) => {
              setReason(e.target.value);
              setMissing(false);
            }}
            aria-invalid={missing ? true : undefined}
            aria-describedby={missing ? 'reason-error' : 'reason-hint'}
          />
          {missing ? (
            <span id="reason-error" className="field-error">
              请填写原因
            </span>
          ) : (
            <span id="reason-hint" className="muted small">
              会记入操作审计，最多 200 字
            </span>
          )}
        </div>
      )}
      {props.error && (
        <p className="field-error" role="alert">
          {props.error}
        </p>
      )}
      <div className="dialog-actions">
        <button type="button" className="button" onClick={props.onCancel} autoFocus disabled={props.busy}>
          取消
        </button>
        <button type="button" className={`button ${props.danger ? 'danger' : 'primary'}`} onClick={confirm} disabled={props.busy}>
          {props.busy ? '处理中…' : props.confirmText}
        </button>
      </div>
    </dialog>
  );
}
