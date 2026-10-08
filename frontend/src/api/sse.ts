import { ApiError, requestRaw } from './http';

// 服务端推送事件（text/event-stream）的基础客户端：导购流式回答用（docs/03）。
// 事件顺序：message_start → 0..n 个 thinking / text_delta / block / followups → 恰好一个 message_done 或 error。

export interface StreamEvent {
  event: string;
  data: Record<string, unknown>;
}

// SSEParser 按 text/event-stream 规则把文本拼成事件：空行结束一个事件；多行 data 用换行连接；
// 冒号开头的是注释（心跳）；没有 data 的事件忽略；event 缺省为 message。不完整的行留到下次。
export class SSEParser {
  private buffer = '';
  private event = '';
  private data: string[] | null = null;

  push(text: string): StreamEvent[] {
    this.buffer += text;
    const out: StreamEvent[] = [];
    let idx: number;
    while ((idx = this.buffer.indexOf('\n')) >= 0) {
      let line = this.buffer.slice(0, idx);
      this.buffer = this.buffer.slice(idx + 1);
      if (line.endsWith('\r')) line = line.slice(0, -1);
      const ev = this.line(line);
      if (ev) out.push(ev);
    }
    return out;
  }

  private line(line: string): StreamEvent | null {
    if (line === '') {
      const data = this.data;
      const name = this.event || 'message';
      this.event = '';
      this.data = null;
      if (data === null) return null;
      let parsed: Record<string, unknown> = {};
      try {
        const v: unknown = JSON.parse(data.join('\n'));
        if (v && typeof v === 'object') parsed = v as Record<string, unknown>;
      } catch {
        parsed = { text: data.join('\n') };
      }
      return { event: name, data: parsed };
    }
    if (line.startsWith(':')) return null;
    const colon = line.indexOf(':');
    const field = colon < 0 ? line : line.slice(0, colon);
    let value = colon < 0 ? '' : line.slice(colon + 1);
    if (value.startsWith(' ')) value = value.slice(1);
    if (field === 'event') this.event = value;
    else if (field === 'data') (this.data ??= []).push(value);
    return null;
  }
}

function isEventStream(res: Response): boolean {
  const type = (res.headers.get('Content-Type') ?? '').split(';')[0].trim().toLowerCase();
  return type === 'text/event-stream';
}

// readEventStream 逐块读取响应并回调每个事件，直到流结束。成功状态但不是事件流（如代理返回的 HTML）视为错误。
export async function readEventStream(res: Response, onEvent: (e: StreamEvent) => void): Promise<void> {
  if (!isEventStream(res) || !res.body) {
    throw new ApiError(res.status, { code: 'bad_response', message: '服务器返回的数据无法识别，请稍后重试' });
  }
  const reader = res.body.getReader();
  const decoder = new TextDecoder('utf-8');
  const parser = new SSEParser();
  for (;;) {
    const { done, value } = await reader.read();
    if (done) break;
    for (const ev of parser.push(decoder.decode(value, { stream: true }))) onEvent(ev);
  }
  for (const ev of parser.push(decoder.decode())) onEvent(ev);
}

export interface AgentMessageInput {
  client_message_id: string;
  content: string;
  attachments?: { file_id: string }[];
}

// streamAgentMessage 向导购会话发送一条消息并流式接收回答。中止 signal 会断开连接，服务端随之把这次运行记为已取消。
// 开始流式输出前的错误（校验失败、会话不存在、未登录）按普通接口错误抛出 ApiError。
export async function streamAgentMessage(
  sessionId: string,
  input: AgentMessageInput,
  onEvent: (e: StreamEvent) => void,
  signal?: AbortSignal,
): Promise<void> {
  const res = await requestRaw(`/agent/sessions/${encodeURIComponent(sessionId)}/messages:stream`, {
    method: 'POST',
    body: JSON.stringify(input),
    headers: { Accept: 'text/event-stream' },
    signal,
  });
  await readEventStream(res, onEvent);
}
