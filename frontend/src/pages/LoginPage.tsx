import { useEffect, useRef, useState, type FormEvent } from 'react';
import { login } from '../api/auth';
import { ApiError } from '../api/http';
import { Field } from '../components/FormField';
import { invalid } from '../lib/invalid';
import { homeHref, navigate } from '../lib/router';
import { saveSession, useSession } from '../lib/session';

type Errors = { username?: string; password?: string };

function validate(username: string, password: string): Errors {
  const errors: Errors = {};
  if (!username.trim()) errors.username = '请输入账号';
  if (!password) errors.password = '请输入密码';
  return errors;
}

// 登录页：先在本地检查必填（错误定位到字段并聚焦第一个），再提交；服务端错误（账号或密码错误、账号停用/风控、限流）原样提示。
// 登录成功回到原地址（next），没有则按角色进入默认页面；已登录时直接跳走。
export function LoginPage({ next }: { next: string }) {
  const session = useSession();
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [errors, setErrors] = useState<Errors>({});
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);
  const usernameRef = useRef<HTMLInputElement>(null);
  const passwordRef = useRef<HTMLInputElement>(null);

  useEffect(() => {
    if (session && !inFlight.current) navigate(next || homeHref(session.account.role));
  }, [session, next]);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (inFlight.current) return;
    const found = validate(username, password);
    setErrors(found);
    setError('');
    if (found.username || found.password) {
      (found.username ? usernameRef : passwordRef).current?.focus();
      return;
    }
    inFlight.current = true;
    setBusy(true);
    try {
      const s = await login(username.trim(), password);
      saveSession(s);
      navigate(next || homeHref(s.account.role));
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '登录失败，请稍后重试');
      // 焦点回到密码框并选中内容，直接重新输入。
      passwordRef.current?.focus();
      passwordRef.current?.select();
    } finally {
      inFlight.current = false;
      setBusy(false);
    }
  }

  return (
    <section className="login" aria-labelledby="login-title">
      <h2 id="login-title">登录</h2>
      <form onSubmit={submit} className="form" noValidate>
        {error && (
          <p className="form-error" role="alert">
            {error}
          </p>
        )}
        <Field label="账号" id="login-username" required error={errors.username}>
          <input
            id="login-username"
            ref={usernameRef}
            autoComplete="username"
            value={username}
            maxLength={32}
            onChange={(e) => setUsername(e.target.value)}
            {...invalid(errors.username, 'login-username')}
          />
        </Field>
        <Field label="密码" id="login-password" required error={errors.password}>
          <input
            id="login-password"
            ref={passwordRef}
            type="password"
            autoComplete="current-password"
            value={password}
            maxLength={128}
            onChange={(e) => setPassword(e.target.value)}
            {...invalid(errors.password, 'login-password')}
          />
        </Field>
        <button type="submit" className="button primary" disabled={busy} aria-busy={busy}>
          {busy ? '登录中…' : '登录'}
        </button>
      </form>
    </section>
  );
}
