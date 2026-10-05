import { render, screen, waitFor, within } from '@testing-library/react';
import userEvent from '@testing-library/user-event';
import { afterEach, describe, expect, it, vi } from 'vitest';
import { useNotices } from '../lib/notice';
import { renderHook } from '@testing-library/react';
import { signIn } from '../test/session';
import { PromotionFormPage } from './PromotionFormPage';
import { ReviewsPage } from './ReviewsPage';

type Route = (url: string, init: RequestInit) => [number, unknown] | undefined;

function serve(route: Route) {
  const fetch = vi.fn(async (url: string, init: RequestInit = {}) => {
    const [status, body] = route(url, init) ?? [404, { code: 'not_found', message: '接口不存在' }];
    return new Response(JSON.stringify(body), { status, headers: { 'Content-Type': 'application/json' } });
  });
  vi.stubGlobal('fetch', fetch);
  return fetch;
}

const page = <T,>(items: T[]) => ({ items, page: 1, page_size: 20, total: items.length });

afterEach(() => vi.unstubAllGlobals());

describe('新建促销', () => {
  function base(url: string): [number, unknown] | undefined {
    if (url.startsWith('/api/v1/merchant/products')) return [200, page([{ product_id: 'p_1', name: '台灯', status: 'active' }])];
    if (url === '/api/v1/categories/tree') return [200, { items: [{ category_id: 'c_1', name: '家居', children: [{ category_id: 'c_2', name: '灯具', children: [] }] }] }];
    return undefined;
  }

  it('本地校验定位到字段，不发请求；折扣按折数填写并换算成实付比例提交', async () => {
    signIn('merchant');
    const posts: unknown[] = [];
    const fetch = serve((url, init) => {
      if (url === '/api/v1/merchant/promotions' && init.method === 'POST') {
        posts.push(JSON.parse(init.body as string));
        return [201, { promotion_id: 'promo_1', name: '灯具 9.5 折' }];
      }
      return base(url);
    });
    render(<PromotionFormPage />);
    const submit = await screen.findByRole('button', { name: '创建促销' });
    await userEvent.click(submit);
    expect(screen.getByText('请填写促销名称')).toBeTruthy();
    expect(screen.getByText('请填写大于 0 的减免金额')).toBeTruthy();
    await waitFor(() => expect(document.activeElement).toBe(screen.getByLabelText(/名称/))); // 下一帧聚焦第一个出错字段
    expect(fetch.mock.calls.some(([, init]) => (init as RequestInit)?.method === 'POST')).toBe(false);

    await userEvent.type(screen.getByLabelText(/名称/), '灯具 9.5 折');
    await userEvent.click(screen.getByLabelText('品类'));
    await userEvent.selectOptions(screen.getByLabelText(/分类/), 'c_2');
    await userEvent.click(screen.getByLabelText('折扣'));
    await userEvent.type(screen.getByLabelText(/折数/), '9.5');
    await userEvent.click(screen.getByLabelText(/可以与其他促销/));
    await userEvent.click(submit);
    await waitFor(() => expect(window.location.hash).toBe('#/merchant/promotions'));
    expect(posts).toEqual([
      { name: '灯具 9.5 折', scope: 'category', category_id: 'c_2', type: 'discount', discount_rate: '0.9500', threshold_amount: '0', stackable: false, status: 'active' },
    ]);
    const { result } = renderHook(() => useNotices());
    expect(result.current.map((n) => n.message)).toEqual(['已创建「灯具 9.5 折」']);
  });

  it('服务端字段错误定位到对应输入框，按钮可再次提交', async () => {
    signIn('merchant');
    serve((url, init) =>
      url === '/api/v1/merchant/promotions' && init.method === 'POST'
        ? [400, { code: 'invalid_argument', message: '只能选择本店的商品', field: 'product_id' }]
        : base(url),
    );
    render(<PromotionFormPage />);
    await userEvent.type(await screen.findByLabelText(/名称/), '台灯立减');
    await userEvent.click(screen.getByLabelText('单品'));
    await userEvent.selectOptions(screen.getByLabelText(/商品/), 'p_1');
    await userEvent.type(screen.getByLabelText(/减免金额/), '10');
    await userEvent.click(screen.getByRole('button', { name: '创建促销' }));
    const product = screen.getByLabelText(/商品/);
    await waitFor(() => expect(product.getAttribute('aria-invalid')).toBe('true'));
    expect(screen.getByRole('alert').textContent).toBe('只能选择本店的商品');
    expect(screen.getByRole('button', { name: '创建促销' })).toHaveProperty('disabled', false);
  });
});

describe('评价回复', () => {
  const review = {
    review_id: 'rv_1', order_id: 'o_1', product_id: 'p_1', product_name: '台灯', sku_id: 's', reviewer_name: '演***', rating: 4,
    content: '光线柔和', tags: ['护眼'], status: 'hidden', merchant_reply: '', merchant_replied_at: null, created_at: '2026-10-01T00:00:00Z',
  };

  it('空回复本地拦截；提交后提示并刷新成已回复；提交中按钮禁用', async () => {
    signIn('merchant');
    let replied = false;
    let release: () => void = () => {};
    const fetch = serve((url, init) => {
      if (url.startsWith('/api/v1/merchant/reviews?') || url === '/api/v1/merchant/reviews') {
        return [200, page([replied ? { ...review, merchant_reply: '谢谢', merchant_replied_at: '2026-10-02T00:00:00Z' } : review])];
      }
      if (url === '/api/v1/merchant/reviews/rv_1:reply' && init.method === 'POST') {
        replied = true;
        return [200, {}];
      }
      return undefined;
    });
    render(<ReviewsPage query={new URLSearchParams()} />);
    const item = await screen.findByRole('article', { name: '台灯 的评价' });
    expect(within(item).getByText('已被平台隐藏')).toBeTruthy();
    expect(within(item).getByLabelText('4 分（满分 5 分）')).toBeTruthy();
    await userEvent.click(within(item).getByRole('button', { name: '提交回复' }));
    expect(within(item).getByRole('alert').textContent).toBe('回复内容需要 1 到 500 个字');
    expect(fetch.mock.calls.filter(([u]) => String(u).includes(':reply'))).toHaveLength(0);

    fetch.mockImplementationOnce(
      () =>
        new Promise((resolve) => {
          release = () => {
            replied = true;
            resolve(new Response('{}', { status: 200 }));
          };
        }),
    );
    await userEvent.type(within(item).getByLabelText('回复'), '谢谢');
    expect(within(item).getByText('2 / 500')).toBeTruthy();
    await userEvent.click(within(item).getByRole('button', { name: '提交回复' }));
    expect(within(item).getByRole('button', { name: '提交中…' })).toHaveProperty('disabled', true);
    release();
    const updated = await screen.findByText('商家回复：');
    expect(updated.parentElement?.textContent).toBe('商家回复：谢谢');
    expect(screen.getByRole('button', { name: '修改回复' })).toBeTruthy();
  });
});
