import type { ReactNode } from 'react';

export function Field(props: { label: string; id: string; required?: boolean; error?: string; hint?: string; children: ReactNode }) {
  return (
    <div className="field">
      <label htmlFor={props.id}>
        {props.label}
        {props.required && <span className="required">*</span>}
      </label>
      {props.children}
      {props.hint && <span className="muted small">{props.hint}</span>}
      {props.error && (
        <span id={`${props.id}-error`} className="field-error">
          {props.error}
        </span>
      )}
    </div>
  );
}
