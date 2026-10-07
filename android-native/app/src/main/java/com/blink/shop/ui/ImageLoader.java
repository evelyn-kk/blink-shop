package com.blink.shop.ui;

import android.content.Context;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.os.Handler;
import android.os.Looper;
import android.util.LruCache;
import android.widget.ImageView;

import java.io.File;
import java.util.concurrent.ExecutorService;
import java.util.concurrent.Executors;

import com.blink.shop.R;
import com.blink.shop.net.ApiConfig;

import okhttp3.Cache;
import okhttp3.OkHttpClient;
import okhttp3.Request;
import okhttp3.Response;
import okhttp3.ResponseBody;

/**
 * 图片加载：内存 LRU + OkHttp 磁盘缓存（按服务端缓存头），按控件大小降采样。
 * 用 tag 记录控件当前要显示的地址，列表复用控件时不会显示错位的旧图。加载失败保留占位背景。
 */
public final class ImageLoader {

    private static final int DISK_CACHE_BYTES = 20 * 1024 * 1024;

    private final OkHttpClient http;
    private final ApiConfig config;
    private final LruCache<String, Bitmap> memory;
    private final ExecutorService pool = Executors.newFixedThreadPool(3);
    private final Handler main = new Handler(Looper.getMainLooper());

    public ImageLoader(Context context, OkHttpClient base, ApiConfig config) {
        this.http = base.newBuilder().cache(new Cache(new File(context.getCacheDir(), "images"), DISK_CACHE_BYTES)).build();
        this.config = config;
        int maxKb = (int) (Runtime.getRuntime().maxMemory() / 1024 / 8);
        memory = new LruCache<String, Bitmap>(maxKb) {
            @Override
            protected int sizeOf(String key, Bitmap value) {
                return value.getByteCount() / 1024;
            }
        };
    }

    /** pathOrUrl 可以是服务端返回的站内路径；targetPx 为期望的最长边像素。 */
    public void load(ImageView view, String pathOrUrl, int targetPx) {
        String url = config.resolve(pathOrUrl);
        view.setTag(R.id.image_url_tag, url);
        view.setImageDrawable(null);
        if (url == null) {
            return;
        }
        String key = url + "@" + targetPx;
        Bitmap cached = memory.get(key);
        if (cached != null) {
            view.setImageBitmap(cached);
            return;
        }
        pool.execute(() -> {
            Bitmap bmp = fetch(url, targetPx);
            if (bmp == null) {
                return;
            }
            memory.put(key, bmp);
            main.post(() -> {
                if (url.equals(view.getTag(R.id.image_url_tag))) {
                    view.setImageBitmap(bmp);
                }
            });
        });
    }

    public void clear() {
        memory.evictAll();
    }

    private Bitmap fetch(String url, int targetPx) {
        try (Response r = http.newCall(new Request.Builder().url(url).build()).execute()) {
            ResponseBody body = r.body();
            if (!r.isSuccessful() || body == null) {
                return null;
            }
            byte[] bytes = body.bytes();
            return decode(bytes, targetPx);
        } catch (Exception e) {
            return null;
        }
    }

    static Bitmap decode(byte[] bytes, int targetPx) {
        BitmapFactory.Options bounds = new BitmapFactory.Options();
        bounds.inJustDecodeBounds = true;
        BitmapFactory.decodeByteArray(bytes, 0, bytes.length, bounds);
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) {
            return null;
        }
        BitmapFactory.Options opts = new BitmapFactory.Options();
        opts.inSampleSize = sampleSize(Math.max(bounds.outWidth, bounds.outHeight), targetPx);
        return BitmapFactory.decodeByteArray(bytes, 0, bytes.length, opts);
    }

    /** 最大的 2 的幂，使降采样后的最长边仍不小于目标。 */
    static int sampleSize(int longest, int targetPx) {
        int s = 1;
        if (targetPx <= 0) {
            return s;
        }
        while (longest / (s * 2) >= targetPx) {
            s *= 2;
        }
        return s;
    }
}
