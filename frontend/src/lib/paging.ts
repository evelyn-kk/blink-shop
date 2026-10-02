// 与服务端 readPage 的规则一致（backend/src/httpapi/catalog.go queryInt）。
const MAX_PAGE = 100000;
const INT64_MAX = 9223372036854775807n;

/**
 * 按服务端规则解析地址栏页码：去掉首尾空白后必须是可选正负号加十进制数字（Go strconv.Atoi），
 * 否则（含 1e5、0x10、1.9、超出 int64）取第 1 页；合法整数限定在 [1, MAX_PAGE]。
 */
export function parsePage(raw: string | null): number {
  const text = (raw ?? '').trim();
  if (!/^[+-]?\d+$/.test(text)) return 1;
  const n = BigInt(text);
  if (n > INT64_MAX || n < -INT64_MAX - 1n) return 1;
  if (n < 1n) return 1;
  return n > BigInt(MAX_PAGE) ? MAX_PAGE : Number(n);
}
