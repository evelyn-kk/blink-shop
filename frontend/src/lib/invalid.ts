/** 出错时给输入框加上 aria-invalid，并关联错误说明。 */
export function invalid(error: string | undefined, id: string) {
  return error ? { 'aria-invalid': true as const, 'aria-describedby': `${id}-error` } : {};
}
