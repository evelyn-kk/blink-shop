import { readFileSync } from 'node:fs';
import path from 'node:path';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { ApiError } from './http';
import { SSEParser, readEventStream, streamAgentMessage, type StreamEvent } from './sse';
import { signIn } from '../test/session';

// 用后端录制的流式 fixture（TestHTTPFixtures 用真实服务验证过）检查解析。
interface EventFixture {
  request: { body: unknown };
  response: { status: number; events: { event: string; data: Record<string, unknown> }[] };
}
const fixture = JSON.parse(
  readFileSync(path.resolve(process.cwd(), '../backend/fixtures/http/agent_message_stream_ok.json'), 'utf8'),
) as EventFixture;

// 把录制的事件还原成服务端会写出的字节（含心跳注释和 CRLF 行尾的变化）。
function wire(events: EventFixture['response']['events']): string {
  return events.map((e, i) => `${i === 2 ? ': ping\r\n\r\n' : ''}event: ${e.event}\ndata: ${JSON.stringify(e.data)}\n\n`).join('');
}

// 按任意字节位置切开（包括切在中文字符中间）。
function streamOf(text: string, chunk: number): ReadableStream<Uint8Array> {
  const bytes = new TextEncoder().encode(text);
  return new ReadableStream({
    start(c) {
      for (let i = 0; i < bytes.length; i += chunk) c.enqueue(bytes.slice(i, i + chunk));
      c.close();
    },
  });
}

afterEach(() => vi.unstubAllGlobals());

describe('SSEParser', () => {
  it('多行 data、注释、缺省事件名、不完整的末尾事件', () => {
    const p = new SSEParser();
    const out: StreamEvent[] = [];
    out.push(...p.push(': ping\n\nevent: a\nda'));
    out.push(...p.push('ta: {"x":1}\n\ndata: 第一行\ndata: 第二行\n\nevent: b\ndata: {}'));
    expect(out).toEqual([
      { event: 'a', data: { x: 1 } },
      { event: 'message', data: { text: '第一行\n第二行' } },
    ]);
  });
});

describe('readEventStream', () => {
  it.each([1, 3, 7, 64, 100000])('录制的回答按 %i 字节切开也能完整解析', async (chunk) => {
    const res = new Response(streamOf(wire(fixture.response.events), chunk), {
      headers: { 'Content-Type': 'text/event-stream; charset=utf-8' },
    });
    const got: StreamEvent[] = [];
    await readEventStream(res, (e) => got.push(e));
    expect(got).toEqual(fixture.response.events);
    expect(got[0].event).toBe('message_start');
    expect(got[got.length - 1].event).toBe('message_done');
  });

  it('成功状态但不是事件流时报错，不当作正常结束', async () => {
    for (const type of ['application/json', 'text/html; charset=utf-8']) {
      const res = new Response('{"x":1}', { headers: { 'Content-Type': type } });
      const err = await readEventStream(res, () => {}).catch((e: unknown) => e);
      expect(err).toBeInstanceOf(ApiError);
      expect((err as ApiError).code).toBe('bad_response');
    }
  });
});

describe('streamAgentMessage', () => {
  it('带 token 和 Accept，路径编码会话 ID；校验错误按普通接口错误抛出', async () => {
    signIn('user', 3600_000, 'tok-u');
    const fetch = vi.fn(async () =>
      new Response(JSON.stringify({ code: 'invalid_argument', message: '请输入 1–2000 个字的问题', field: 'content' }), {
        status: 400,
        headers: { 'Content-Type': 'application/json' },
      }),
    );
    vi.stubGlobal('fetch', fetch);
    const err = await streamAgentMessage('s_1', { client_message_id: 'c1', content: '' }, () => {}).catch((e: unknown) => e);
    expect(err).toBeInstanceOf(ApiError);
    expect((err as ApiError).field).toBe('content');
    const [url, init] = fetch.mock.calls[0] as unknown as [string, RequestInit];
    expect(url).toBe('/api/v1/agent/sessions/s_1/messages:stream');
    const headers = new Headers(init.headers);
    expect(headers.get('Authorization')).toBe('Bearer tok-u');
    expect(headers.get('Accept')).toBe('text/event-stream');
    expect(JSON.parse(init.body as string)).toEqual({ client_message_id: 'c1', content: '' });
  });

  it('正常流：逐个回调事件', async () => {
    signIn('user', 3600_000, 'tok-u');
    vi.stubGlobal(
      'fetch',
      vi.fn(async () => new Response(streamOf(wire(fixture.response.events), 5), { headers: { 'Content-Type': 'text/event-stream' } })),
    );
    const names: string[] = [];
    await streamAgentMessage('s_1', fixture.request.body as { client_message_id: string; content: string }, (e) => names.push(e.event));
    expect(names[0]).toBe('message_start');
    expect(names.filter((n) => n === 'text_delta').length).toBeGreaterThan(1);
    expect(names[names.length - 1]).toBe('message_done');
  });
});
