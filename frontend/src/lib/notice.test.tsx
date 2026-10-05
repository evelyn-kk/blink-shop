import { act, render, screen } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { NoticeHost } from '../components/NoticeHost';
import { AUTO_DISMISS_MS, notify } from './notice';

afterEach(() => vi.useRealTimers());

describe('全局通知', () => {
  it('成功和提示自动消失，错误一直保留到手动关闭', () => {
    vi.useFakeTimers();
    render(<NoticeHost />);
    act(() => {
      notify('success', '已保存');
      notify('error', '保存失败');
    });
    expect(screen.getByRole('status')).toHaveProperty('textContent', '完成已保存×');
    expect(screen.getByRole('alert').textContent).toContain('保存失败');
    act(() => vi.advanceTimersByTime(AUTO_DISMISS_MS));
    expect(screen.queryByRole('status')).toBeNull();
    expect(screen.getByRole('alert')).toBeTruthy();
  });

  it('关闭按钮可以用键盘操作，相同提示不重复堆叠，最多同时 3 条', async () => {
    const user = userEvent.setup();
    render(<NoticeHost />);
    act(() => {
      notify('error', '甲');
      notify('error', '甲');
    });
    expect(screen.getAllByRole('alert')).toHaveLength(1);
    act(() => {
      for (const m of ['乙', '丙', '丁']) notify('error', m);
    });
    expect(screen.getAllByRole('alert').map((n) => n.textContent)).toEqual(['出错了乙×', '出错了丙×', '出错了丁×']);
    await user.tab();
    expect(document.activeElement).toBe(screen.getAllByRole('button', { name: '关闭提示' })[0]);
    await user.keyboard('{Enter}');
    expect(screen.getAllByRole('alert')).toHaveLength(2);
  });
});
