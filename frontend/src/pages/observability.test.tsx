import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { signIn } from '../test/session';
import { AdminDebugPage } from './AdminDebugPage';
import { AdminEvalsPage } from './AdminEvalsPage';

type Handler = (url: string, init: RequestInit) => [number, unknown] | undefined;

function serve(handler: Handler) {
  const calls: string[] = [];
  const fetch = vi.fn(async (url: string, init: RequestInit = {}) => {
    calls.push(`${init.method ?? 'GET'} ${url}`);
    const [status, body] = handler(url, init) ?? [404, { code: 'not_found', message: '接口不存在' }];
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  });
  vi.stubGlobal('fetch', fetch);
  return calls;
}

afterEach(() => vi.unstubAllGlobals());

const at = '2026-10-01T08:00:00Z';
const run = (id: string, status: string, query: string) => ({
  run_id: id, session_id: 's1', account_id: 'acct_u1', username: 'blink_user', trace_id: `tr_${id}`, status, query, attachments: 0,
  prompt_versions: { 'agent.react': 2 }, error_code: status === 'failed' ? 'agent_timeout' : '', elapsed_ms: status === 'running' ? null : 1200,
  created_at: at, updated_at: at,
});
const event = (stage: string, type: string, status: string, extra: Record<string, unknown> = {}) => ({
  trace_event_id: `${stage}.${type}`, stage, event_type: type, model: '', status, duration_ms: 3, error: '', metadata: {}, created_at: at, ...extra,
});

describe('调试观测', () => {
  it('筛选条件进请求；点开运行看汇总和时间线，失败事件和错误标出来', async () => {
    signIn('admin');
    const calls = serve((url) => {
      if (url.startsWith('/api/v1/admin/agent/runs?')) {
        return [200, { items: [run('run_a', 'failed', '耳机保修多久？我电话 [手机号]'), run('run_b', 'running', '推荐耳机')], page: 1, page_size: 20, total: 2 }];
      }
      if (url === '/api/v1/admin/agent/runs/run_a') {
        return [200, {
          ...run('run_a', 'failed', '耳机保修多久？我电话 [手机号]'), content: '耳机保修一年', blocks: [], followups: [],
          summary: { event_count: 3, failed_count: 1, events_ms: 9, models: ['deepseek-chat'], tools: ['search_knowledge'], chunk_ids: ['ck_1'], product_ids: [] },
          items: [event('run', 'start', 'ok'), event('retrieval', 'knowledge', 'ok', { metadata: { chunk_ids: ['ck_1'] } }),
            event('answer', 'model', 'error', { model: 'deepseek-chat', error: 'timeout' })],
        }];
      }
      return undefined;
    });
    const { rerender } = render(<AdminDebugPage query={new URLSearchParams('status=failed&account_id=acct_u1')} />);
    const row = (await screen.findByText('耳机保修多久？我电话 [手机号]')).closest('tr')!;
    expect(calls[0]).toBe('GET /api/v1/admin/agent/runs?status=failed&account_id=acct_u1&page_size=20');
    expect(within(row).getByText('失败')).toBeTruthy();
    expect(within(row).getByText('1.2 s')).toBeTruthy();
    expect(within(screen.getByText('推荐耳机').closest('tr')!).getByText('—')).toBeTruthy(); // 运行中没有耗时
    expect(within(row).getByRole('link', { name: '查看运行 run_a' }).getAttribute('href')).toBe('#/admin/debug?status=failed&account_id=acct_u1&run=run_a');

    rerender(<AdminDebugPage query={new URLSearchParams('status=failed&account_id=acct_u1&run=run_a')} />);
    const detail = (await screen.findByRole('heading', { name: '运行 run_a' })).closest('section')!;
    await within(detail).findByText('错误：agent_timeout');
    const failedStat = within(detail).getByText('失败事件').closest('.stat-card')!;
    expect(failedStat.className).toContain('stat-bad');
    const steps = within(detail).getAllByRole('listitem');
    expect(steps).toHaveLength(3);
    expect(steps[2].className).toContain('tone-border-bad');
    expect(within(steps[2]).getByText('timeout')).toBeTruthy();
    expect(within(detail).getByText('Prompt 版本：agent.react v2')).toBeTruthy();
    expect(within(detail).getByText('引用的分块：ck_1')).toBeTruthy();
  });

  it('筛选表单提交后改地址', async () => {
    signIn('admin');
    serve((url) => (url.startsWith('/api/v1/admin/agent/runs?') ? [200, { items: [], page: 1, page_size: 20, total: 0 }] : undefined));
    render(<AdminDebugPage query={new URLSearchParams()} />);
    await screen.findByText('没有符合条件的运行');
    await userEvent.selectOptions(screen.getByLabelText('状态'), 'completed');
    await userEvent.type(screen.getByLabelText('账号 ID'), ' acct_x ');
    await userEvent.click(screen.getByRole('button', { name: '查找' }));
    expect(window.location.hash).toBe('#/admin/debug?status=completed&account_id=acct_x');
  });
});

const summary = (id: string, suite: string, gate: string, passed: number, total: number) => ({
  id, suite, version: 'abc123', config_fingerprint: 'f00', started_at: at, total, passed, pass_rate: passed / total, min_pass_rate: suite === 'intent' ? 0.9 : 1,
  gate, failure_count: total - passed,
});

describe('质量评测', () => {
  const overview = {
    configured: true,
    suites: [
      { id: 'intent', name: '意图识别', dataset: 'intent', min_pass_rate: 0.9, command: 'go run ./cmd/eval -suite=intent', try_path: '/api/v1/eval/intent', case_count: 66,
        latest: summary('eval-2/intent', 'intent', 'failed', 50, 66) },
      { id: 'agent', name: '端到端导购', dataset: 'agent', min_pass_rate: 1, command: 'go run ./cmd/eval -suite=agent', try_path: '', case_count: 26, latest: null },
    ],
    datasets: [{ id: 'intent', suite: 'intent', description: '意图评测集', case_count: 66, updated_at: at }],
    reports: [summary('eval-2/intent', 'intent', 'failed', 50, 66), summary('eval-1/rag', 'rag', 'passed', 38, 38)],
    reports_total: 2,
    skipped: 1,
  };
  const vector = {
    knowledge: { enabled: false, backend: '', embedder: '', collection: null },
    products: { enabled: true, backend: 'milvus', embedder: 'text-embedding-v3', collection: { name: 'blink_shop_products', exists: true, dimension: 1024, row_count: 6, load_state: 'LoadStateLoaded', error: '' } },
    images: { enabled: true, backend: 'milvus', embedder: 'local', collection: { name: 'blink_shop_product_images', exists: false, dimension: 0, row_count: 0, load_state: '', error: 'unreachable' } },
    sync: { running: 0, succeeded: 6, failed: 1, last_at: at, last_error_at: at },
    checked_at: at,
  };
  const report = {
    ...summary('eval-2/intent', 'intent', 'failed', 1, 2), schema_version: 'blink_shop.eval_report.v1', config: { planner: 'rule' }, duration_ms: 3,
    metrics: { intent_accuracy: 0.5 }, groups: { cart: { total: 1, passed: 0, pass_rate: 0 }, guide: { total: 1, passed: 1, pass_rate: 1 } },
    cases: [{ id: 'in_001', group: 'guide', passed: true }, { id: 'in_002', group: 'cart', passed: false }],
    failures: [{ id: 'in_002', group: 'cart', query: '把购物车里的耳机删了', problems: ['动作应为 remove'], got: ['cart/view'] }],
  };

  it('未过门槛的套件和报告标红；报告详情默认只看失败用例，可切换全部；向量异常和同步失败标出', async () => {
    signIn('admin');
    serve((url) => {
      if (url === '/api/v1/admin/evals') return [200, overview];
      if (url === '/api/v1/admin/vector/status') return [200, vector];
      if (url === '/api/v1/admin/evals/reports/eval-2/intent') return [200, report];
      return undefined;
    });
    render(<AdminEvalsPage query={new URLSearchParams('report=eval-2/intent')} />);
    const card = (await screen.findByRole('heading', { name: '意图识别' })).closest('.stat-card')!;
    expect(card.className).toContain('eval-failed');
    expect(within(card as HTMLElement).getByText('未过门槛')).toBeTruthy();
    expect(within(screen.getByRole('heading', { name: '端到端导购' }).closest('.stat-card')! as HTMLElement).getByText('还没有报告')).toBeTruthy();
    expect(screen.getByText('另有 1 份旧格式或无法读取的报告没有列出。')).toBeTruthy();
    const rows = screen.getByRole('table', { name: '评测报告' }).querySelectorAll('tbody tr');
    expect(within(rows[0] as HTMLElement).getByText('未过门槛')).toBeTruthy();
    expect(within(rows[1] as HTMLElement).getByText('通过')).toBeTruthy();

    const detail = (await screen.findByRole('heading', { name: '报告 eval-2/intent' })).closest('section')!;
    await within(detail).findByText('把购物车里的耳机删了');
    expect(within(detail).getByText('动作应为 remove')).toBeTruthy();
    expect(within(detail).queryByText('in_001')).toBeNull(); // 未过门槛：默认只看失败
    expect(within(detail).getByText('in_002').closest('li')!.className).toContain('eval-failed');
    await userEvent.click(within(detail).getByLabelText(/只看失败/));
    expect(within(detail).getByText('in_001')).toBeTruthy();
    expect(within(within(detail).getByRole('table', { name: '分组通过情况' })).getByText('0/1')).toBeTruthy();

    const vec = await screen.findByRole('table', { name: '向量索引状态' });
    expect(within(vec).getByText('未启用')).toBeTruthy();
    expect(within(vec).getByText('LoadStateLoaded')).toBeTruthy();
    expect(within(vec).getByText('异常：unreachable')).toBeTruthy();
    expect(screen.getByText(/成功 6，失败 1/).className).toContain('field-error');
  });

  it('单条试跑：按类型调对应接口，显示结果；服务端错误显示原因', async () => {
    signIn('admin');
    const posts: { url: string; body: unknown }[] = [];
    serve((url, init) => {
      if (url === '/api/v1/admin/evals') return [200, { ...overview, reports: [], suites: [] }];
      if (url === '/api/v1/admin/vector/status') return [200, vector];
      if (init.method === 'POST') {
        const body = JSON.parse(init.body as string);
        posts.push({ url, body });
        if (url === '/api/v1/eval/products' && body.query.length > 100) return [400, { code: 'invalid_argument', message: 'query 最多 100 个字符', field: 'query' }];
        if (url === '/api/v1/eval/intent') return [200, { query: body.query, plan: { intent: 'cart', action: 'add', rule: 'cart_add' }, duration_ms: 0 }];
        return [200, { query: body.query, tool: 'search_products', ok: true, data: { products: [{ product_id: 'p_seed_earbuds' }] }, duration_ms: 2 }];
      }
      return undefined;
    });
    render(<AdminEvalsPage query={new URLSearchParams()} />);
    const panel = (await screen.findByRole('heading', { name: '单条试跑' })).closest('section')!;
    const input = within(panel).getByLabelText('输入');
    await userEvent.type(input, '把第一个加入购物车');
    await userEvent.click(within(panel).getByRole('button', { name: '试跑' }));
    expect((await within(panel).findByLabelText('试跑结果')).textContent).toContain('cart_add');
    await userEvent.selectOptions(within(panel).getByLabelText('类型'), 'products');
    await userEvent.clear(input);
    await userEvent.type(input, '降噪耳机');
    await userEvent.click(within(panel).getByRole('button', { name: '试跑' }));
    await waitFor(() => expect(within(panel).getByLabelText('试跑结果').textContent).toContain('p_seed_earbuds'));
    await userEvent.clear(input);
    await userEvent.type(input, '长'.repeat(101));
    await userEvent.click(within(panel).getByRole('button', { name: '试跑' }));
    expect((await within(panel).findByRole('alert')).textContent).toBe('query 最多 100 个字符');
    expect(posts.map((p) => p.url)).toEqual(['/api/v1/eval/intent', '/api/v1/eval/products', '/api/v1/eval/products']);
  });
});
