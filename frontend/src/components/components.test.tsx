import { act, fireEvent, render, screen, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { describe, expect, it, vi } from 'vitest';
import { signIn } from '../test/session';
import { ConfirmDialog } from './ConfirmDialog';
import { DataTable } from './DataTable';
import { PageLinks } from './PageLinks';
import { RequireRole } from './RequireRole';
import { Empty, ErrorState, Loading } from './StateView';
import { StatusBadge } from './StatusBadge';

describe('StateView', () => {
  it('加载、空态、错误都有可读文字，错误可以重试', async () => {
    const retry = vi.fn();
    render(
      <>
        <Loading />
        <Empty text="还没有订单" />
        <ErrorState message="服务暂时不可用" onRetry={retry} />
      </>,
    );
    expect(screen.getByRole('status').textContent).toBe('加载中…');
    expect(screen.getByText('还没有订单')).toBeTruthy();
    expect(screen.getByRole('alert').textContent).toContain('服务暂时不可用');
    await userEvent.click(screen.getByRole('button', { name: '重试' }));
    expect(retry).toHaveBeenCalledOnce();
  });
});

describe('StatusBadge', () => {
  it('文字就是状态，颜色只做辅助', () => {
    render(<StatusBadge tone="bad">风控审核中</StatusBadge>);
    const badge = screen.getByText('风控审核中');
    expect(badge.className).toBe('status tone-bad');
  });
});

describe('DataTable', () => {
  const rows = [
    { id: 'o1', no: 'BS1', amount: '12.00' },
    { id: 'o2', no: 'BS2', amount: '3.50' },
  ];
  const columns = [
    { key: 'no', header: '订单号', render: (r: (typeof rows)[number]) => r.no },
    { key: 'amount', header: '实付', align: 'end' as const, render: (r: (typeof rows)[number]) => `¥${r.amount}` },
  ];

  it('列头带 scope，单元格带列名（窄屏卡片用），数字右对齐', () => {
    render(<DataTable caption="订单" columns={columns} rows={rows} rowKey={(r) => r.id} empty="没有订单" />);
    const table = screen.getByRole('table', { name: '订单' });
    const headers = within(table).getAllByRole('columnheader');
    expect(headers.map((h) => [h.textContent, h.getAttribute('scope')])).toEqual([
      ['订单号', 'col'],
      ['实付', 'col'],
    ]);
    const cells = within(table).getAllByRole('cell');
    expect(cells.map((c) => c.getAttribute('data-label'))).toEqual(['订单号', '实付', '订单号', '实付']);
    expect(cells[1].className).toBe('num');
    expect(cells[3].textContent).toBe('¥3.50');
  });

  it('没有数据时显示空态而不是空表格', () => {
    render(<DataTable caption="订单" columns={columns} rows={[]} rowKey={(r) => r.id} empty="没有订单" />);
    expect(screen.queryByRole('table')).toBeNull();
    expect(screen.getByText('没有订单')).toBeTruthy();
  });
});

describe('PageLinks', () => {
  it('只有一页时不显示；中间页有上一页和下一页', () => {
    const href = (n: number) => `#/p?page=${n}`;
    const { container, rerender } = render(<PageLinks page={1} total={10} pageSize={10} href={href} />);
    expect(container.textContent).toBe('');
    rerender(<PageLinks page={2} total={25} pageSize={10} href={href} />);
    expect(screen.getByRole('navigation', { name: '分页' }).textContent).toContain('第 2 / 3 页');
    expect(screen.getByRole('link', { name: '上一页' }).getAttribute('href')).toBe('#/p?page=1');
    expect(screen.getByRole('link', { name: '下一页' }).getAttribute('href')).toBe('#/p?page=3');
  });
  it('页码超出末页时显示末页', () => {
    render(<PageLinks page={9} total={25} pageSize={10} href={(n) => `#${n}`} />);
    expect(screen.getByText('第 3 / 3 页')).toBeTruthy();
    expect(screen.getByRole('link', { name: '上一页' }).getAttribute('href')).toBe('#3');
    expect(screen.queryByRole('link', { name: '下一页' })).toBeNull();
  });
});

describe('ConfirmDialog', () => {
  function setup(busy = false) {
    const onConfirm = vi.fn();
    const onCancel = vi.fn();
    render(
      <ConfirmDialog open title="删除商品" message="删除后无法恢复" confirmText="确认删除" danger busy={busy} onConfirm={onConfirm} onCancel={onCancel} />,
    );
    return { onConfirm, onCancel, dialog: screen.getByRole('dialog', { name: '删除商品' }) };
  }

  it('打开后显示说明，确认和取消分别回调', async () => {
    const { onConfirm, onCancel, dialog } = setup();
    expect(dialog.hasAttribute('open')).toBe(true);
    expect(dialog.textContent).toContain('删除后无法恢复');
    await userEvent.click(within(dialog).getByRole('button', { name: '确认删除' }));
    await userEvent.click(within(dialog).getByRole('button', { name: '取消' }));
    expect(onConfirm).toHaveBeenCalledOnce();
    expect(onCancel).toHaveBeenCalledOnce();
  });

  it('Esc 等同取消（不直接关掉对话框，由页面决定）', () => {
    const { onCancel, dialog } = setup();
    const event = new Event('cancel', { cancelable: true });
    fireEvent(dialog, event);
    expect(onCancel).toHaveBeenCalledOnce();
    expect(event.defaultPrevented).toBe(true);
  });

  it('处理中两个按钮都禁用，防止重复提交', () => {
    const { dialog } = setup(true);
    const buttons = within(dialog).getAllByRole('button');
    expect(buttons.map((b) => [b.textContent, (b as HTMLButtonElement).disabled])).toEqual([
      ['取消', true],
      ['处理中…', true],
    ]);
  });
});

describe('RequireRole', () => {
  it('未登录：跳到登录页并记住原地址', () => {
    window.location.hash = '#/admin/orders?status=paid';
    render(
      <RequireRole role="admin">
        <p>订单管理</p>
      </RequireRole>,
    );
    expect(screen.getByRole('status').textContent).toBe('请先登录…');
    expect(screen.queryByText('订单管理')).toBeNull();
    expect(window.location.hash).toBe('#/login?next=%23%2Fadmin%2Forders%3Fstatus%3Dpaid');
  });

  it('角色不符：提示无权访问，不渲染页面', () => {
    signIn('merchant');
    render(
      <RequireRole role="admin">
        <p>订单管理</p>
      </RequireRole>,
    );
    expect(screen.getByText('当前账号不是管理员，无权访问平台管理')).toBeTruthy();
    expect(screen.queryByText('订单管理')).toBeNull();
  });

  it('页面里切换过地址后会话才失效：登录后回到切换后的地址', () => {
    signIn('admin');
    window.location.hash = '#/admin/orders';
    render(
      <RequireRole role="admin">
        <p>订单管理</p>
      </RequireRole>,
    );
    window.location.hash = '#/admin/orders?status=paid';
    act(() => window.localStorage.removeItem('blink_shop.session'));
    act(() => window.dispatchEvent(new StorageEvent('storage')));
    expect(window.location.hash).toBe('#/login?next=%23%2Fadmin%2Forders%3Fstatus%3Dpaid');
  });

  it('角色相符：渲染页面；会话失效后立即跳回登录页', () => {
    signIn('admin');
    window.location.hash = '#/admin/orders';
    render(
      <RequireRole role="admin">
        <p>订单管理</p>
      </RequireRole>,
    );
    expect(screen.getByText('订单管理')).toBeTruthy();
    act(() => window.localStorage.removeItem('blink_shop.session'));
    act(() => window.dispatchEvent(new StorageEvent('storage')));
    expect(screen.queryByText('订单管理')).toBeNull();
    expect(window.location.hash).toBe('#/login?next=%23%2Fadmin%2Forders');
  });
});
