import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { signIn } from '../test/session';
import { AdminConfigsPage } from './AdminConfigsPage';
import { AdminPlatformPage } from './AdminPlatformPage';
import { AdminPromptsPage } from './AdminPromptsPage';

type Handler = (url: string, init: RequestInit) => [number, unknown] | undefined;

function serve(handler: Handler) {
  const fetch = vi.fn(async (url: string, init: RequestInit = {}) => {
    const [status, body] = handler(url, init) ?? [404, { code: 'not_found', message: '接口不存在' }];
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

const page = <T,>(items: T[]) => ({ items, page: 1, page_size: 20, total: items.length });
const account = (id: string, username: string, status = 'active') => ({
  account_id: id, username, display_name: `${username} 的名字`, role: 'user', merchant_id: '', status, created_at: '2026-10-01T00:00:00Z', updated_at: '2026-10-01T00:00:00Z',
});

afterEach(() => vi.unstubAllGlobals());

describe('平台管理', () => {
  it('自己的账号没有状态操作；风控要写原因，提交的请求带上原因', async () => {
    signIn('admin'); // account_id = acct_admin
    let rows = [account('acct_admin', 'me'), account('acct_u1', 'alice')];
    const patches: unknown[] = [];
    serve((url, init) => {
      if (url.startsWith('/api/v1/admin/accounts?') || url === '/api/v1/admin/accounts') return [200, page(rows)];
      if (url === '/api/v1/admin/accounts/acct_u1' && init.method === 'PATCH') {
        patches.push(JSON.parse(init.body as string));
        rows = [rows[0], { ...rows[1], status: 'risk' }];
        return [200, rows[1]];
      }
      return undefined;
    });
    render(<AdminPlatformPage query={new URLSearchParams()} />);
    const mine = (await screen.findByText('me')).closest('tr')!;
    expect(within(mine).queryAllByRole('button')).toHaveLength(0);
    const alice = screen.getByText('alice').closest('tr')!;
    await userEvent.click(within(alice).getByRole('button', { name: '设为风控 alice' }));
    const dialog = screen.getByRole('dialog');
    expect(dialog.textContent).toContain('风控后该账号只能浏览');
    await userEvent.click(within(dialog).getByRole('button', { name: '确认设为风控' }));
    expect(within(dialog).getByText('请填写原因')).toBeTruthy();
    expect(patches).toHaveLength(0);
    await userEvent.type(within(dialog).getByLabelText(/原因/), '疑似刷单');
    await userEvent.click(within(dialog).getByRole('button', { name: '确认设为风控' }));
    await waitFor(() => expect(patches).toEqual([{ status: 'risk', reason: '疑似刷单' }]));
    await waitFor(() => expect(within(screen.getByText('alice').closest('tr')!).getByText('风控')).toBeTruthy());
    expect(within(screen.getByText('alice').closest('tr')!).getByRole('link', { name: '查看 alice 的操作记录' }).getAttribute('href')).toBe(
      '#/admin/audit?target_type=account&target_id=acct_u1',
    );
  });

  it('恢复正常不需要原因；服务端拒绝时原因显示在对话框里', async () => {
    signIn('admin');
    serve((url, init) => {
      if (url.startsWith('/api/v1/admin/accounts')) {
        if (init.method === 'PATCH') return [409, { code: 'status_unchanged', message: '已经是这个状态' }];
        return [200, page([account('acct_u1', 'alice', 'risk')])];
      }
      return undefined;
    });
    render(<AdminPlatformPage query={new URLSearchParams()} />);
    await userEvent.click(await screen.findByRole('button', { name: '恢复正常 alice' }));
    const dialog = screen.getByRole('dialog');
    expect(within(dialog).queryByLabelText(/原因/)).toBeNull();
    await userEvent.click(within(dialog).getByRole('button', { name: '确认恢复正常' }));
    expect((await within(dialog).findByRole('alert')).textContent).toBe('已经是这个状态');
  });

  it('已删除的商品没有状态操作', async () => {
    signIn('admin');
    serve((url) =>
      url.startsWith('/api/v1/admin/products')
        ? [200, page([{ product_id: 'p1', merchant_id: 'm', merchant_name: '店', category_id: 'c', name: '旧手机', image_url: '', price: '1.00', stock_quantity: 0,
            stock_status: 'out_of_stock', status: 'deleted', updated_at: '2026-10-01T00:00:00Z' }])]
        : undefined,
    );
    render(<AdminPlatformPage query={new URLSearchParams('tab=products')} />);
    const row = (await screen.findByText('旧手机')).closest('tr')!;
    expect(within(row).getByText('已删除')).toBeTruthy();
    expect(within(row).queryAllByRole('button')).toHaveLength(0);
  });
});

describe('应用配置', () => {
  it('密钥只读且显示掩码；可调配置可以保存和恢复默认', async () => {
    signIn('admin');
    const items = [
      { key: 'ai.api_key', env: 'AI_API_KEY', description: '模型服务 API Key', value: '******', source: 'env', secret: true, editable: false },
      { key: 'http.request_timeout', env: 'HTTP_REQUEST_TIMEOUT', description: '超时', value: '45s', source: 'dynamic', secret: false, editable: true },
    ];
    const patches: unknown[] = [];
    serve((url, init) => {
      if (url === '/api/v1/admin/configs') return [200, { items }];
      if (url === '/api/v1/admin/configs/http.request_timeout' && init.method === 'PATCH') {
        patches.push(JSON.parse(init.body as string));
        return [200, items[1]];
      }
      return undefined;
    });
    render(<AdminConfigsPage />);
    const secret = await screen.findByLabelText('ai.api_key');
    expect(secret).toHaveProperty('value', '******');
    expect(secret).toHaveProperty('readOnly', true);
    expect(screen.getByText('只读：密钥不在后台显示或修改')).toBeTruthy();
    const timeout = screen.getByLabelText('http.request_timeout');
    const save = within(timeout.closest('.config-row')! as HTMLElement).getByRole('button', { name: '保存' });
    expect(save).toHaveProperty('disabled', true); // 没改动不能保存
    await userEvent.clear(timeout);
    await userEvent.type(timeout, '60s');
    await userEvent.click(save);
    await waitFor(() => expect(patches).toHaveLength(1)); // 保存完成、按钮恢复后再点
    const reset = within(screen.getByLabelText('http.request_timeout').closest('.config-row')! as HTMLElement).getByRole('button', { name: '恢复默认' });
    await waitFor(() => expect(reset).toHaveProperty('disabled', false));
    await userEvent.click(reset);
    await waitFor(() => expect(patches).toEqual([{ value: '60s' }, { value: '' }]));
  });
});

describe('应用配置的存放位置', () => {
  it('Nacos 不可用时提示修改不会生效；没配 Nacos 时说明存在内存', async () => {
    signIn('admin');
    serve((url) => (url === '/api/v1/admin/configs' ? [200, { items: [], backend: { kind: 'nacos', healthy: false, checked_at: '2026-10-01T08:00:00Z' } }] : undefined));
    const { unmount } = render(<AdminConfigsPage />);
    expect(await screen.findByText('后台设置存在 Nacos')).toBeTruthy();
    expect(screen.getByRole('status').textContent).toContain('此时修改不会生效');
    unmount();
    serve((url) => (url === '/api/v1/admin/configs' ? [200, { items: [], backend: { kind: 'memory', healthy: true, checked_at: null } }] : undefined));
    render(<AdminConfigsPage />);
    expect(await screen.findByText('后台设置存在内存')).toBeTruthy();
  });
});

describe('Prompt 管理', () => {
  const at = '2026-10-01T08:00:00Z';
  const version = (v: number, status: string, content: string) => ({
    prompt_id: `prompt_${v}`, version: v, status, title: '回答', description: '', content, runes: [...content].length, created_by: 'acct_admin',
    created_at: at, updated_at: at, published_at: status === 'draft' ? null : at,
  });
  const summary = { prompt_key: 'agent.react', title: '回答', description: '导购回答', placeholders: ['{{TOOLS}}'] };

  function promptServer() {
    let detail = {
      ...summary, active_version: 1, active_source: 'published', version_count: 2, builtin_content: '内置 {{TOOLS}}',
      draft: { ...version(2, 'draft', ''), content: undefined } as Partial<ReturnType<typeof version>> | null, // 和接口一致：摘要里的草稿不带内容
      versions: [version(2, 'draft', '草稿 {{TOOLS}}'), version(1, 'active', '一号 {{TOOLS}}')],
      publish_records: [] as unknown[],
    };
    const calls: { url: string; body: unknown }[] = [];
    let conflict = false;
    const fetch = serve((url, init) => {
      if (url === '/api/v1/admin/prompts') return [200, { items: [{ ...detail, versions: undefined, publish_records: undefined }] }];
      if (url === '/api/v1/admin/prompts/agent.react' && !init.method) return [200, detail];
      const body = init.body ? JSON.parse(init.body as string) : undefined;
      calls.push({ url, body });
      if (url === '/api/v1/admin/prompts/agent.react' && init.method === 'PATCH') {
        if (!body.content.includes('{{TOOLS}}')) return [400, { code: 'validation_failed', message: '必须包含 {{TOOLS}}', field: 'content' }];
        const saved = { ...version(2, 'draft', body.content), updated_at: '2026-10-01T09:00:00Z' };
        detail = { ...detail, draft: { ...saved, content: undefined }, versions: [saved, detail.versions[1]] };
        return [200, saved];
      }
      if (url.endsWith('/publish') || url.endsWith('/rollback')) {
        if (conflict) return [409, { code: 'prompt_active_changed', message: '生效版本已被其他人修改，请刷新后确认再操作' }];
        const record = { record_id: `rec_${calls.length}`, version: body.version, from_version: detail.active_version, action: url.endsWith('/publish') ? 'publish' : 'rollback',
          published_by: 'acct_admin', reason: body.reason, result: url.endsWith('/publish') ? 'fallback' : 'success', created_at: at };
        detail = {
          ...detail, active_version: body.version, active_source: body.version === 0 ? 'builtin' : 'published', draft: null,
          versions: [version(2, body.version === 2 ? 'active' : 'archived', '草稿 {{TOOLS}}'), version(1, body.version === 1 ? 'active' : 'archived', '一号 {{TOOLS}}')],
          publish_records: [record, ...detail.publish_records],
        };
        return [200, { prompt: detail, record }];
      }
      return undefined;
    });
    return { calls, fetch, setConflict: (v: boolean) => (conflict = v) };
  }

  it('保存草稿要包含占位符；发布带上看到的生效版本，配置中心推送失败时标出来', async () => {
    signIn('admin');
    const { calls } = promptServer();
    render(<AdminPromptsPage query={new URLSearchParams()} />);
    const editor = await screen.findByLabelText('编辑草稿 v2');
    expect(editor).toHaveProperty('value', '草稿 {{TOOLS}}');
    expect(screen.getByText('生效：v1')).toBeTruthy();
    await userEvent.clear(editor);
    await userEvent.type(editor, '没有占位符');
    expect(screen.getByRole('button', { name: '发布草稿 v2' })).toHaveProperty('disabled', true); // 未保存不能发布
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    expect((await screen.findByRole('alert')).textContent).toContain('必须包含 {{TOOLS}}');
    await userEvent.clear(editor);
    await userEvent.type(editor, '新草稿 {{{{TOOLS}}');
    await userEvent.click(screen.getByRole('button', { name: '保存草稿' }));
    await waitFor(() => expect(calls.at(-1)).toEqual({ url: '/api/v1/admin/prompts/agent.react', body: { content: '新草稿 {{TOOLS}}' } }));
    await userEvent.click(await screen.findByRole('button', { name: '发布草稿 v2' }));
    const dialog = screen.getByRole('dialog');
    expect(dialog.textContent).toContain('替换当前生效的v1');
    await userEvent.click(within(dialog).getByRole('button', { name: '确认发布' }));
    await waitFor(() => expect(calls.at(-1)).toEqual({ url: '/api/v1/admin/prompts/agent.react/publish', body: { version: 2, expected_active_version: 1, reason: '' } }));
    expect(await screen.findByText('已生效，配置中心推送失败')).toBeTruthy();
    expect(screen.getByText('生效：v2')).toBeTruthy();
    expect(screen.getByText('v1 → v2')).toBeTruthy();
  });

  it('回滚必须写原因；别人先改了会提示刷新；可以恢复内置默认', async () => {
    signIn('admin');
    const { calls, setConflict } = promptServer();
    render(<AdminPromptsPage query={new URLSearchParams('key=agent.react')} />);
    await userEvent.click(await screen.findByRole('button', { name: '发布草稿 v2' }));
    await userEvent.click(within(screen.getByRole('dialog')).getByRole('button', { name: '确认发布' }));
    await userEvent.click(await screen.findByRole('button', { name: '回滚到 v1' }));
    let dialog = screen.getByRole('dialog');
    await userEvent.click(within(dialog).getByRole('button', { name: '确认回滚' }));
    expect(within(dialog).getByText('请填写原因')).toBeTruthy();
    setConflict(true);
    await userEvent.type(within(dialog).getByLabelText(/原因/), '新版本回答变差');
    await userEvent.click(within(dialog).getByRole('button', { name: '确认回滚' }));
    expect((await within(dialog).findByRole('alert')).textContent).toContain('请刷新后确认再操作');
    setConflict(false);
    await userEvent.click(within(dialog).getByRole('button', { name: '取消' }));
    await userEvent.click(await screen.findByRole('button', { name: '恢复内置默认' }));
    dialog = screen.getByRole('dialog');
    await userEvent.type(within(dialog).getByLabelText(/原因/), '回到默认');
    await userEvent.click(within(dialog).getByRole('button', { name: '确认回滚' }));
    await waitFor(() =>
      expect(calls.at(-1)).toEqual({ url: '/api/v1/admin/prompts/agent.react/rollback', body: { version: 0, expected_active_version: 2, reason: '回到默认' } }),
    );
    expect(await screen.findByText('生效：内置默认')).toBeTruthy();
    expect(screen.queryByRole('button', { name: '恢复内置默认' })).toBeNull();
  });
});
