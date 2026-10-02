import { useEffect, useRef } from 'react';

// 二次确认对话框：原生 <dialog> 模态，Esc 等同取消，焦点默认落在“取消”上，避免误操作。
export function ConfirmDialog(props: {
  open: boolean;
  title: string;
  message: string;
  confirmText: string;
  danger?: boolean;
  busy?: boolean;
  onConfirm: () => void;
  onCancel: () => void;
}) {
  const ref = useRef<HTMLDialogElement>(null);
  const { open, onCancel } = props;

  useEffect(() => {
    const dialog = ref.current;
    if (!dialog) return;
    if (open && !dialog.open) dialog.showModal();
    if (!open && dialog.open) dialog.close();
  }, [open]);

  return (
    <dialog
      ref={ref}
      className="dialog"
      aria-labelledby="confirm-title"
      onCancel={(e) => {
        e.preventDefault();
        onCancel();
      }}
    >
      <h3 id="confirm-title">{props.title}</h3>
      <p>{props.message}</p>
      <div className="dialog-actions">
        <button type="button" className="button" onClick={onCancel} autoFocus disabled={props.busy}>
          取消
        </button>
        <button
          type="button"
          className={`button ${props.danger ? 'danger' : 'primary'}`}
          onClick={props.onConfirm}
          disabled={props.busy}
        >
          {props.busy ? '处理中…' : props.confirmText}
        </button>
      </div>
    </dialog>
  );
}
