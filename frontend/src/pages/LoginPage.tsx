import { useRef, useState, type FormEvent } from 'react';
import { login } from '../api/auth';
import { ApiError } from '../api/http';
import { navigate, productsHref, merchantProductsHref } from '../lib/router';
import { saveSession } from '../lib/session';

export function LoginPage({ next }: { next: string }) {
  const [username, setUsername] = useState('');
  const [password, setPassword] = useState('');
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const inFlight = useRef(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    if (inFlight.current) return;
    inFlight.current = true;
    setBusy(true);
    setError('');
    try {
      const session = await login(username.trim(), password);
      saveSession(session);
      const home = session.account.role === 'merchant' ? merchantProductsHref() : productsHref({});
      navigate(next || home);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : '登录失败，请稍后重试');
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
        <label className="field">
          <span>账号</span>
          <input autoComplete="username" value={username} required onChange={(e) => setUsername(e.target.value)} />
        </label>
        <label className="field">
          <span>密码</span>
          <input
            type="password"
            autoComplete="current-password"
            value={password}
            required
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        <button type="submit" className="button primary" disabled={busy || !username.trim() || !password}>
          {busy ? '登录中…' : '登录'}
        </button>
      </form>
    </section>
  );
}
