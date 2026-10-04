import { useState } from 'react';
import { ApiError } from '../api/http';
import { cancelOrderAsAdmin, shipOrder, type OrderScope } from '../api/orders';
import type { Order } from '../types/api';
import { ConfirmDialog } from './ConfirmDialog';

type Action = 'ship' | 'cancel';

// 订单操作按钮：待发货的订单可以发货；管理员还可以取消待支付订单。都需要二次确认，处理中按钮禁用，不会重复提交。
// 结果（成功提示或失败原因）交给 onDone，由页面刷新数据：失败多半是订单状态已被别人改了，刷新后按最新状态显示。
export function OrderActions(props: {
  scope: OrderScope;
  order: Order;
  onDone: (result: { ok: boolean; message: string }) => void;
}) {
  const { scope, order, onDone } = props;
  const [pending, setPending] = useState<Action | null>(null);
  const [busy, setBusy] = useState(false);
  const canShip = order.status === 'paid';
  const canCancel = scope === 'admin' && order.status === 'pending_payment';
  if (!canShip && !canCancel) return null;

  async function confirm() {
    if (!pending || busy) return;
    setBusy(true);
    try {
      if (pending === 'ship') {
        await shipOrder(scope, order.order_id);
        onDone({ ok: true, message: `订单 ${order.order_no} 已发货` });
      } else {
        await cancelOrderAsAdmin(order.order_id, '平台取消');
        onDone({ ok: true, message: `订单 ${order.order_no} 已取消，库存已回补` });
      }
    } catch (err) {
      onDone({ ok: false, message: err instanceof ApiError ? err.message : '操作失败，请稍后重试' });
    } finally {
      setBusy(false);
      setPending(null);
    }
  }

  return (
    <>
      {canShip && (
        <button type="button" className="button primary" aria-label={`发货 ${order.order_no}`} onClick={() => setPending('ship')}>
          发货
        </button>
      )}
      {canCancel && (
        <button type="button" className="button danger-outline" aria-label={`取消订单 ${order.order_no}`} onClick={() => setPending('cancel')}>
          取消订单
        </button>
      )}
      <ConfirmDialog
        open={pending !== null}
        title={pending === 'ship' ? '确认发货' : pending === 'cancel' ? '取消订单' : ''}
        message={
          // 对话框关闭时不放文字：它渲染在列表行里，关闭状态的文字会混进这一行的文本。
          pending === 'ship'
            ? `确认订单 ${order.order_no} 已发货吗？发货后买家可以确认收货。`
            : pending === 'cancel'
              ? `确定取消订单 ${order.order_no} 吗？订单关闭后库存回补、优惠券退回给买家，不能恢复。`
              : ''
        }
        confirmText={pending === 'ship' ? '确认发货' : '确认取消'}
        danger={pending === 'cancel'}
        busy={busy}
        onConfirm={confirm}
        onCancel={() => setPending(null)}
      />
    </>
  );
}
