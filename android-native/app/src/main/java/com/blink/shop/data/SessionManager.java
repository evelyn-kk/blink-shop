package com.blink.shop.data;

import java.util.concurrent.CopyOnWriteArraySet;
import java.util.concurrent.Executor;

import com.blink.shop.model.Account;
import com.blink.shop.model.Session;
import com.blink.shop.net.AuthInterceptor;

/**
 * 当前登录会话（线程安全）。网络层通过 SessionSource 取 token、报告 401；
 * 会话变化通过 listenerExecutor（App 里是主线程）通知界面。
 */
public final class SessionManager implements AuthInterceptor.SessionSource {

    /** 会话的持久化。 */
    public interface Storage {
        Session load();

        void save(Session session);

        void clear();
    }

    public interface Listener {
        /** expired 为 true 表示服务端判定登录失效（而不是用户主动退出）。 */
        void onSessionChanged(Session session, boolean expired);
    }

    public interface Clock {
        long now();
    }

    private final Storage storage;
    private final Executor listenerExecutor;
    private final Clock clock;
    private final CopyOnWriteArraySet<Listener> listeners = new CopyOnWriteArraySet<>();
    private Session session;

    /** 冷启动时从存储恢复；本地已知过期的会话直接丢弃。 */
    public SessionManager(Storage storage, Executor listenerExecutor, Clock clock) {
        this.storage = storage;
        this.listenerExecutor = listenerExecutor;
        this.clock = clock;
        Session restored = storage.load();
        if (restored != null && (restored.token.isEmpty() || restored.isExpired(clock.now()))) {
            storage.clear();
            restored = null;
        }
        session = restored;
    }

    public synchronized Session current() {
        return session;
    }

    public boolean isLoggedIn() {
        return current() != null;
    }

    public void addListener(Listener l) {
        listeners.add(l);
    }

    public void removeListener(Listener l) {
        listeners.remove(l);
    }

    public void signIn(Session s) {
        synchronized (this) {
            session = s;
            storage.save(s);
        }
        notifyChanged(s, false);
    }

    /** 用最新的账户资料更新会话；登录态已变（退出或换号）时忽略。 */
    public void updateAccount(String token, Account account) {
        Session updated;
        synchronized (this) {
            if (session == null || !session.token.equals(token)) {
                return;
            }
            session = session.withAccount(account);
            storage.save(session);
            updated = session;
        }
        notifyChanged(updated, false);
    }

    public void signOut() {
        synchronized (this) {
            if (session == null) {
                return;
            }
            session = null;
            storage.clear();
        }
        notifyChanged(null, false);
    }

    @Override
    public synchronized String token() {
        return session == null ? null : session.token;
    }

    @Override
    public void onUnauthorized(String rejectedToken) {
        synchronized (this) {
            if (session == null || !session.token.equals(rejectedToken)) {
                return;
            }
            session = null;
            storage.clear();
        }
        notifyChanged(null, true);
    }

    private void notifyChanged(Session s, boolean expired) {
        for (Listener l : listeners) {
            listenerExecutor.execute(() -> l.onSessionChanged(s, expired));
        }
    }
}
