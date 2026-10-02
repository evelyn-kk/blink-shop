// 跨页面的一次性提示（如“已保存”）：写入后由下一个页面读取，并在挂载后清除。
// 读取和清除分开：开发模式下 StrictMode 会调用两次 state 初始化函数，读取不能有副作用。
let pending = '';

export function setFlash(message: string): void {
  pending = message;
}

export function peekFlash(): string {
  return pending;
}

export function clearFlash(): void {
  pending = '';
}
