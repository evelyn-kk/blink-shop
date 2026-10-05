import { saveSession } from '../lib/session';
import type { Role, Session } from '../types/api';

/** 写入一个测试会话。expiresInMs 为负数时得到已过期的会话。 */
export function signIn(role: Role, expiresInMs = 3600_000, token = 'test-token'): Session {
  const s: Session = {
    token,
    token_type: 'Bearer',
    expires_at: new Date(Date.now() + expiresInMs).toISOString(),
    account: {
      account_id: `acct_${role}`,
      username: `${role}_user`,
      display_name: `测试${role}`,
      avatar_url: '',
      phone: '',
      email: '',
      role,
      merchant_id: role === 'merchant' ? 'm_seed_digital' : '',
      status: 'active',
      created_at: '2026-10-01T00:00:00Z',
      updated_at: '2026-10-01T00:00:00Z',
    },
  };
  saveSession(s);
  return s;
}
