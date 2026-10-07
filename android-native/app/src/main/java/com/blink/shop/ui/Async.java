package com.blink.shop.ui;

import android.os.Handler;
import android.os.Looper;

import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import com.blink.shop.net.ApiException;

/** 在后台线程执行接口调用，结果回到主线程；取消后不再回调（页面销毁时取消）。 */
public final class Async {

    public interface Task<T> {
        T run() throws ApiException;
    }

    public interface Callback<T> {
        void onSuccess(T value);

        void onError(ApiException e);
    }

    public static final class Handle {
        private volatile boolean canceled;

        public void cancel() {
            canceled = true;
        }

        public boolean isCanceled() {
            return canceled;
        }
    }

    private static final ExecutorService POOL = Executors.newFixedThreadPool(4);
    private static final Handler MAIN = new Handler(Looper.getMainLooper());

    private Async() {
    }

    public static <T> Handle run(Task<T> task, Callback<T> callback) {
        Handle h = new Handle();
        POOL.execute(() -> {
            T value = null;
            ApiException error = null;
            try {
                value = task.run();
            } catch (ApiException e) {
                error = e;
            } catch (RuntimeException e) {
                error = ApiException.badResponse(e);
            }
            final T v = value;
            final ApiException err = error;
            MAIN.post(() -> {
                if (h.canceled) {
                    return;
                }
                if (err != null) {
                    callback.onError(err);
                } else {
                    callback.onSuccess(v);
                }
            });
        });
        return h;
    }
}
