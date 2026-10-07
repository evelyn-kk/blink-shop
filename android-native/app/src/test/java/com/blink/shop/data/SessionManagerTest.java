package com.blink.shop.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.List;

import org.json.JSONObject;
import org.junit.Test;

import com.blink.shop.model.Account;
import com.blink.shop.model.Session;

public class SessionManagerTest {

    private static final class MemoryStorage implements SessionManager.Storage {
        Session saved;
        int clears;

        @Override
        public Session load() {
            return saved;
        }

        @Override
        public void save(Session s) {
            saved = s;
        }

        @Override
        public void clear() {
            saved = null;
            clears++;
        }
    }

    private static Account account(String name) {
        try {
            return Account.fromJson(new JSONObject().put("username", "u").put("display_name", name));
        } catch (org.json.JSONException e) {
            throw new IllegalStateException(e);
        }
    }

    private final List<String> events = new ArrayList<>();

    private SessionManager manager(MemoryStorage storage, long now) {
        SessionManager m = new SessionManager(storage, Runnable::run, () -> now);
        m.addListener((s, expired) -> events.add((s == null ? "null" : s.account.displayName) + (expired ? ":expired" : "")));
        return m;
    }

    @Test
    public void restoresStoredSessionOnColdStart() {
        MemoryStorage st = new MemoryStorage();
        st.saved = new Session("tok", 2_000, account("小蓝"));
        SessionManager m = manager(st, 1_000);
        assertTrue(m.isLoggedIn());
        assertEquals("tok", m.token());
    }

    @Test
    public void dropsLocallyExpiredSession() {
        MemoryStorage st = new MemoryStorage();
        st.saved = new Session("tok", 1_000, account("小蓝"));
        SessionManager m = manager(st, 1_000);
        assertFalse(m.isLoggedIn());
        assertNull(st.saved);
        // 过期时间未知（0）时保留，交给服务端判断
        st.saved = new Session("tok", 0, account("小蓝"));
        assertTrue(manager(st, 5_000).isLoggedIn());
    }

    @Test
    public void unauthorizedClearsOnlyMatchingToken() {
        MemoryStorage st = new MemoryStorage();
        SessionManager m = manager(st, 0);
        m.signIn(new Session("new", 0, account("新")));
        m.onUnauthorized("old"); // 旧 token 的迟到 401 不能清掉新登录
        assertTrue(m.isLoggedIn());
        m.onUnauthorized("new");
        assertFalse(m.isLoggedIn());
        assertNull(st.saved);
        m.onUnauthorized("new"); // 重复通知不再触发
        assertEquals(java.util.Arrays.asList("新", "null:expired"), events);
    }

    @Test
    public void updateAccountIgnoredAfterSwitch() {
        MemoryStorage st = new MemoryStorage();
        SessionManager m = manager(st, 0);
        m.signIn(new Session("a", 0, account("甲")));
        m.updateAccount("b", account("乙"));
        assertEquals("甲", m.current().account.displayName);
        m.updateAccount("a", account("甲2"));
        assertEquals("甲2", st.saved.account.displayName);
        m.signOut();
        m.signOut();
        assertEquals(java.util.Arrays.asList("甲", "甲2", "null"), events);
    }
}
