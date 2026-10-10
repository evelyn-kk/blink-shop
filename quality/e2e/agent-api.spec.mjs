import { expect, test } from '@playwright/test';

// 导购 Agent（无模型、无 AI key）：直接调用真实 API 的 SSE 接口，走通“商品查询 → 把第一个加购物车 → 结算 → 订单查询 → 支付”，
// 并验证两条安全规则：没有上文时“把第一个加购物车”不写购物车；风险词在规划前拦截。每个用例注册新用户，重复运行互不影响。

async function newUser(request, tag) {
  const username = `agent_${tag}_${String(Date.now()).slice(-9)}_${Math.floor(Math.random() * 1e5)}`;
  const res = await request.post('/api/v1/auth/register', {
    data: { username, password: 'E2e-Agent#2026', display_name: '导购测试' },
  });
  expect(res.status(), await res.text()).toBe(201);
  const { token } = await res.json();
  const headers = { Authorization: `Bearer ${token}` };
  const call = async (method, path, data) => {
    const r = await request.fetch(`/api/v1${path}`, { method, data, headers });
    return { status: r.status(), body: await r.json() };
  };
  const session = await call('POST', '/agent/sessions', {});
  expect(session.status).toBe(201);
  let seq = 0;
  // 发一条消息并读完整个事件流（Playwright 的 request 在流结束后返回全部正文）。
  const ask = async (content) => {
    const r = await request.post(`/api/v1/agent/sessions/${session.body.session_id}/messages:stream`, {
      headers: { ...headers, Accept: 'text/event-stream' },
      data: { client_message_id: `e2e-${++seq}`, content },
    });
    expect(r.status(), await r.text()).toBe(200);
    expect(r.headers()['content-type']).toContain('text/event-stream');
    const events = (await r.text())
      .split('\n\n')
      .filter((frame) => frame.startsWith('event: '))
      .map((frame) => {
        const lines = frame.split('\n');
        return { event: lines[0].slice(7), data: JSON.parse(lines.find((l) => l.startsWith('data: ')).slice(6)) };
      });
    expect(events[0].event).toBe('message_start');
    expect(events[events.length - 1].event).toBe('message_done');
    const text = events.filter((e) => e.event === 'text_delta').map((e) => e.data.delta).join('');
    const blocks = events.filter((e) => e.event === 'block').map((e) => e.data.block);
    return { events, text, blocks, runId: events[0].data.run_id };
  };
  const trace = async (runId) => (await call('GET', `/agent/runs/${runId}/trace`)).body.items.map((i) => `${i.stage}.${i.event_type}.${i.status}`);
  return { call, ask, trace };
}

test('无模型闭环：商品查询 → 把第一个加购物车 → 结算 → 订单查询 → 支付', async ({ request }, testInfo) => {
  const u = await newUser(request, testInfo.project.name[0]);

  let r = await u.ask('推荐一款静音无线鼠标');
  const list = r.blocks.find((b) => b.type === 'product_list');
  expect(list.products[0]).toMatchObject({ product_id: 'p_seed_mouse', name: 'Blink 静音无线鼠标 M2', price: '129.00' });
  expect(r.text).toContain('Blink 静音无线鼠标 M2');
  expect(await u.trace(r.runId)).toEqual(['run.start.ok', 'risk.check.ok', 'memory.retrieval.skipped', 'planner.rule.ok', 'tool.search_products.ok', 'retrieval.products.ok', 'rerank.products.ok', 'followup.rule.ok', 'answer.rule.ok', 'memory.summary.ok', 'run.end.completed']);

  r = await u.ask('把第一个加入购物车');
  expect(r.text).toContain('已把 Blink 静音无线鼠标 M2 × 1 加入购物车');
  expect(r.blocks.map((b) => b.type)).toEqual(['cart', 'action']);
  const cart = (await u.call('GET', '/cart')).body;
  expect(cart.items.map((i) => [i.product_id, i.quantity])).toEqual([['p_seed_mouse', 1]]);

  r = await u.ask('结算');
  const orders = r.blocks.find((b) => b.type === 'order_list').orders;
  expect(orders).toHaveLength(1);
  expect(orders[0]).toMatchObject({ status: 'pending_payment', pay_amount: '129.00' });
  expect(r.text).toContain('已为你生成 1 个订单');
  expect((await u.call('GET', '/cart')).body.items).toEqual([]);

  r = await u.ask('我的订单');
  expect(r.text).toContain('你有 1 个订单');
  expect(r.text).toContain(orders[0].order_no);

  r = await u.ask('支付订单');
  expect(r.text).toContain('已完成（模拟）支付');
  const paid = (await u.call('GET', `/orders/${orders[0].order_id}`)).body;
  expect(paid.status).toBe('paid');
  expect(paid.payment.status).toBe('paid');

  // 同一条消息重发：重放保存的结果，不会再支付一次
  const again = await u.ask('我的订单');
  expect(again.text).toContain('待发货');
});

test('安全边界：没有上文不猜商品、风险词在规划前拦截、导航不调用工具', async ({ request }, testInfo) => {
  const u = await newUser(request, `${testInfo.project.name[0]}s`);

  let r = await u.ask('把第一个加入购物车');
  expect(r.text).toContain('我还没有给你推荐过商品');
  expect(r.blocks).toEqual([]);
  expect((await u.call('GET', '/cart')).body.items).toEqual([]);
  expect((await u.trace(r.runId)).filter((s) => s.startsWith('tool.'))).toEqual([]);

  r = await u.ask('有没有假货');
  expect(r.text).toContain('不能继续处理');
  expect(await u.trace(r.runId)).toEqual(['run.start.ok', 'risk.check.blocked', 'followup.rule.ok', 'answer.rule.ok', 'run.end.completed']);

  r = await u.ask('打开购物车页面');
  expect(r.blocks).toEqual([{ type: 'action', action: 'navigate', target: 'cart', label: '去购物车', params: {} }]);
  expect((await u.trace(r.runId)).filter((s) => s.startsWith('tool.'))).toEqual([]);

  r = await u.ask('七天无理由怎么退');
  expect(r.text).toContain('《Blink 数码售后政策》');
  expect(r.blocks.find((b) => b.type === 'citation').citations[0].document_id).toBe('doc_seed_after_sales');
});
