package com.blink.shop;

import android.app.Application;
import android.os.Handler;
import android.os.Looper;

import com.blink.shop.data.AndroidConnectivity;
import com.blink.shop.data.Drafts;
import com.blink.shop.data.ApiBaseStore;
import com.blink.shop.data.PrefsSessionStorage;
import com.blink.shop.data.SessionManager;
import com.blink.shop.data.ShopApi;
import com.blink.shop.model.Account;
import com.blink.shop.model.Session;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.ApiException;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.ImageLoader;

import okhttp3.OkHttpClient;

/** 全局对象：会话、接口客户端和图片加载器。 */
public final class ShopApp extends Application {

    private SessionManager sessions;
    private Drafts drafts;
    private ApiBaseStore apiBase;
    private AndroidConnectivity connectivity;
    private OkHttpClient baseHttp;
    private volatile ShopApi api;
    private volatile ImageLoader images;
    private boolean expiredNotice;
    private int apiVersion;

    @Override
    public void onCreate() {
        super.onCreate();
        Handler main = new Handler(Looper.getMainLooper());
        sessions = new SessionManager(new PrefsSessionStorage(this), main::post, System::currentTimeMillis);
        sessions.addListener((session, expired) -> {
            if (expired) {
                expiredNotice = true;
            }
        });
        apiBase = new ApiBaseStore(this);
        drafts = new Drafts(this);
        connectivity = new AndroidConnectivity(this);
        baseHttp = ApiClient.defaultHttp().build();
        rebuild();
        refreshSessionOnStart();
    }

    public static ShopApp of(android.content.Context c) {
        return (ShopApp) c.getApplicationContext();
    }

    public SessionManager sessions() {
        return sessions;
    }

    public Drafts drafts() {
        return drafts;
    }

    public ShopApi api() {
        return api;
    }

    public ImageLoader images() {
        return images;
    }

    public ApiBaseStore apiBase() {
        return apiBase;
    }

    public AndroidConnectivity connectivity() {
        return connectivity;
    }

    /** 服务地址每切换一次加一；页面据此判断已加载的数据是否来自旧服务器。只在主线程调用。 */
    public int apiVersion() {
        return apiVersion;
    }

    /** 取走“登录已失效”提示（只提示一次）。只在主线程调用。 */
    public boolean consumeExpiredNotice() {
        boolean v = expiredNotice;
        expiredNotice = false;
        return v;
    }

    /** 切换服务地址：旧服务器的登录不再有效，一并退出。base 为 null 表示恢复默认。 */
    public void switchApiBase(String base) {
        apiBase.set(base);
        sessions.signOut();
        rebuild();
    }

    private void rebuild() {
        ApiConfig config = new ApiConfig(apiBase.get());
        api = new ShopApi(new ApiClient(baseHttp, config, sessions, connectivity));
        apiVersion++;
        ImageLoader old = images;
        images = new ImageLoader(this, baseHttp, config);
        if (old != null) {
            old.clear();
        }
    }

    /** 冷启动恢复会话后，向服务端确认 token 仍有效并刷新资料；401 时拦截器会清除会话。 */
    private void refreshSessionOnStart() {
        Session s = sessions.current();
        if (s == null) {
            return;
        }
        Async.run(() -> api.me(), new Async.Callback<Account>() {
            @Override
            public void onSuccess(Account account) {
                sessions.updateAccount(s.token, account);
            }

            @Override
            public void onError(ApiException e) {
                // 网络问题时保留本地会话，下次请求再判断
            }
        });
    }
}
