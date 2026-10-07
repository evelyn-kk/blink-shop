package com.blink.shop.account;

import android.content.ContentResolver;
import android.graphics.Bitmap;
import android.graphics.BitmapFactory;
import android.net.Uri;

import java.io.ByteArrayOutputStream;
import java.io.IOException;
import java.io.InputStream;

/** 把用户选的图片居中裁成正方形、缩到 512px、转成 JPEG，保证不超过头像接口的 2MB 限制。 */
final class AvatarImage {

    static final int SIZE = 512;

    private AvatarImage() {
    }

    static byte[] prepare(ContentResolver resolver, Uri uri) throws IOException {
        BitmapFactory.Options bounds = new BitmapFactory.Options();
        bounds.inJustDecodeBounds = true;
        try (InputStream in = resolver.openInputStream(uri)) {
            BitmapFactory.decodeStream(in, null, bounds);
        }
        if (bounds.outWidth <= 0 || bounds.outHeight <= 0) {
            throw new IOException("not an image");
        }
        BitmapFactory.Options opts = new BitmapFactory.Options();
        int shortest = Math.min(bounds.outWidth, bounds.outHeight);
        int sample = 1;
        while (shortest / (sample * 2) >= SIZE) {
            sample *= 2;
        }
        opts.inSampleSize = sample;
        Bitmap src;
        try (InputStream in = resolver.openInputStream(uri)) {
            src = BitmapFactory.decodeStream(in, null, opts);
        }
        if (src == null) {
            throw new IOException("decode failed");
        }
        int side = Math.min(src.getWidth(), src.getHeight());
        Bitmap square = Bitmap.createBitmap(src, (src.getWidth() - side) / 2, (src.getHeight() - side) / 2, side, side);
        Bitmap scaled = side > SIZE ? Bitmap.createScaledBitmap(square, SIZE, SIZE, true) : square;
        ByteArrayOutputStream out = new ByteArrayOutputStream();
        scaled.compress(Bitmap.CompressFormat.JPEG, 88, out);
        return out.toByteArray();
    }
}
