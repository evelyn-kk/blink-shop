// <input type="datetime-local"> 与接口时间（RFC 3339）之间的转换，按浏览器本地时区。

function pad(n: number): string {
  return String(n).padStart(2, '0');
}

/** 接口时间 → 输入框的值（YYYY-MM-DDTHH:mm，本地时间）；非法时间返回空串。 */
export function toLocalInput(iso: string): string {
  const d = new Date(iso);
  if (Number.isNaN(d.getTime())) return '';
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;
}

/** 输入框的值（本地时间）→ 接口时间（UTC，RFC 3339）；空值或非法返回 undefined。 */
export function fromLocalInput(value: string): string | undefined {
  if (!value) return undefined;
  const d = new Date(value);
  if (Number.isNaN(d.getTime())) return undefined;
  return d.toISOString().replace(/\.\d{3}Z$/, 'Z');
}

/** 折扣率（实付比例，如 "0.9500"）→ 折数（9.5）。 */
export function rateToZhe(rate: string): string {
  const n = Number(rate);
  if (!Number.isFinite(n) || n <= 0) return '';
  return String(Number((n * 10).toFixed(3)));
}

/** 折数（如 "9.5"）→ 折扣率字符串（"0.9500"）；不在 (0, 10) 或超过两位小数返回 undefined。 */
export function zheToRate(zhe: string): string | undefined {
  const text = zhe.trim();
  if (!/^\d+(\.\d{1,2})?$/.test(text)) return undefined;
  const n = Number(text);
  if (!(n > 0 && n < 10)) return undefined;
  return (n / 10).toFixed(4);
}
