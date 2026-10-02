import { useEffect, useState, type ReactNode } from 'react';
import { loginHref, navigate } from '../lib/router';
import { useSession } from '../lib/session';
import { Empty, Loading } from './StateView';

// 前端路由守卫：未登录去登录页，登录后回到原地址；非商家账号提示无权访问。后端仍会独立校验。
export function RequireMerchant({ children }: { children: ReactNode }) {
  const session = useSession();
  // 在渲染时记下当前地址：effect 可能执行多次（StrictMode），第二次执行时地址已经变成登录页。
  const [target] = useState(() => window.location.hash);
  useEffect(() => {
    if (!session) navigate(loginHref(target));
  }, [session, target]);
  if (!session) return <Loading text="请先登录…" />;
  if (session.account.role !== 'merchant') return <Empty text="当前账号不是商家，无权访问商家工作台" />;
  return <>{children}</>;
}
