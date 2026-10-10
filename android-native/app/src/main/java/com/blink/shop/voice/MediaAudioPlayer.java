package com.blink.shop.voice;

import android.content.Context;
import android.media.AudioAttributes;
import android.media.MediaPlayer;

import java.io.File;
import java.io.FileOutputStream;
import java.io.IOException;

/** 用 MediaPlayer 播放合成的音频：写到缓存目录的临时文件再播放，停止 / 播完后删除。 */
public final class MediaAudioPlayer implements TtsController.Player {

    private final File dir;
    private MediaPlayer player;
    private File current;

    public MediaAudioPlayer(Context c) {
        dir = new File(c.getCacheDir(), "tts");
    }

    @Override
    public void play(TtsController.Audio audio, Runnable done, TtsController.ErrorSink error) {
        stop();
        try {
            if (!dir.isDirectory() && !dir.mkdirs()) {
                throw new IOException("cache dir");
            }
            String ext = audio.contentType.contains("wav") ? ".wav" : audio.contentType.contains("ogg") ? ".ogg" : ".mp3";
            current = File.createTempFile("speech", ext, dir);
            try (FileOutputStream out = new FileOutputStream(current)) {
                out.write(audio.data);
            }
            MediaPlayer p = new MediaPlayer();
            p.setAudioAttributes(new AudioAttributes.Builder().setUsage(AudioAttributes.USAGE_MEDIA)
                    .setContentType(AudioAttributes.CONTENT_TYPE_SPEECH).build());
            p.setDataSource(current.getAbsolutePath());
            p.setOnCompletionListener(mp -> {
                stop();
                done.run();
            });
            p.setOnErrorListener((mp, what, extra) -> {
                stop();
                error.onError("这段语音播放失败");
                return true;
            });
            p.prepare();
            p.start();
            player = p;
        } catch (IOException | RuntimeException e) {
            stop();
            error.onError("这段语音播放失败");
        }
    }

    @Override
    public void stop() {
        MediaPlayer p = player;
        player = null;
        if (p != null) {
            try {
                p.stop();
            } catch (IllegalStateException ignored) {
                // 还没开始或已结束
            }
            p.release();
        }
        if (current != null) {
            //noinspection ResultOfMethodCallIgnored
            current.delete();
            current = null;
        }
    }

    @Override
    public void release() {
        stop();
    }
}
