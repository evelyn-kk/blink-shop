import { useEffect, type ReactNode } from 'react';
import { loginHref, navigate } from '../lib/router';
import { useSession } from '../lib/session';
import type { Role } from '../types/api';
import { Empty, Loading } from './StateView';

const denied: Partial<Record<Role, string>> = {
  merchant: '当前账号不是商家，无权访问商家工作台',
  admin: '当前账号不是管理员，无权访问平台管理',
};

// 前端路由守卫：未登录去登录页，登录后回到原地址；角色不符提示无权访问。后端仍会独立校验。
export function RequireRole({ role, children }: { role: Role; children: ReactNode }) {
  const session = useSession();
  // 会话消失时（未登录、退出、失效）跳到登录页，登录后回到“此刻”的地址：页面里切换过筛选的，回到切换后的地址。
  // effect 可能执行两次（StrictMode），第二次时地址已经是登录页，不再跳。
  useEffect(() => {
    const here = window.location.hash;
    if (!session && !here.startsWith('#/login')) navigate(loginHref(here));
  }, [session]);
  if (!session) return <Loading text="请先登录…" />;
  if (session.account.role !== role) return <Empty text={denied[role] ?? '无权访问'} />;
  return <>{children}</>;
}
