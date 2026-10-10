package com.blink.shop.voice;

import java.util.concurrent.Executor;

import com.blink.shop.net.ApiException;

/**
 * 朗读：同一时间只朗读一段（按 key 区分，例如聊天里的一轮）。play 先清洗文本、取音频、交给播放器；再点同一段或调 stop 就停。
 * 取音频过程中停止、或换了一段，迟到的音频被丢弃。页面退出时调 release。纯 Java，不依赖 Android。
 */
public final class TtsController {

    public interface Synthesizer {
        Audio synthesize(String text) throws ApiException;
    }

    public static final class Audio {
        public final byte[] data;
        public final String contentType;

        public Audio(byte[] data, String contentType) {
            this.data = data;
            this.contentType = contentType;
        }
    }

    public interface Player {
        /** 开始播放；播完、出错都回调一次 done（任意线程）。 */
        void play(Audio audio, Runnable done, ErrorSink error);

        void stop();

        void release();
    }

    public interface ErrorSink {
        void onError(String message);
    }

    public interface Listener {
        /** key 的状态变了：loading（取音频中）、playing、或都为 false（空闲）。 */
        void onTtsState(String key, boolean loading, boolean playing);

        void onTtsError(String message);
    }

    private final Synthesizer synth;
    private final Player player;
    private final Executor io;
    private final Executor main;
    private Listener listener;
    private int maxChars = 800;

    private String key = "";
    private boolean loading;
    private boolean playing;
    private int generation;

    public TtsController(Synthesizer synth, Player player, Executor io, Executor main) {
        this.synth = synth;
        this.player = player;
        this.io = io;
        this.main = main;
    }

    public void setListener(Listener l) {
        listener = l;
    }

    public void setMaxChars(int n) {
        if (n > 0) {
            maxChars = n;
        }
    }

    public boolean isActive(String k) {
        return k.equals(key) && (loading || playing);
    }

    public boolean isLoading(String k) {
        return k.equals(key) && loading;
    }

    /** 朗读 text；正在朗读同一个 key 时变为停止。返回 false 表示没开始（文本清洗后为空）。 */
    public boolean toggle(String k, String text) {
        if (isActive(k)) {
            stop();
            return false;
        }
        String clean = TtsText.clean(text, maxChars);
        if (clean.isEmpty()) {
            if (listener != null) {
                listener.onTtsError("这段回答没有可以朗读的文字");
            }
            return false;
        }
        stop();
        generation++;
        final int gen = generation;
        key = k;
        loading = true;
        notifyState();
        io.execute(() -> {
            try {
                Audio audio = synth.synthesize(clean);
                main.execute(() -> {
                    if (gen != generation) {
                        return;
                    }
                    loading = false;
                    playing = true;
                    notifyState();
                    player.play(audio, () -> main.execute(() -> finished(gen)), msg -> main.execute(() -> failed(gen, msg)));
                });
            } catch (ApiException e) {
                main.execute(() -> failed(gen, describe(e)));
            }
        });
        return true;
    }

    /** 停止当前朗读（取音频中或播放中）。 */
    public void stop() {
        if (!loading && !playing) {
            return;
        }
        generation++;
        boolean wasPlaying = playing;
        loading = false;
        playing = false;
        if (wasPlaying) {
            player.stop();
        }
        notifyState();
    }

    /** 页面退出：停止并释放播放器。 */
    public void release() {
        stop();
        player.release();
    }

    private void finished(int gen) {
        if (gen != generation) {
            return;
        }
        playing = false;
        notifyState();
    }

    private void failed(int gen, String message) {
        if (gen != generation) {
            return;
        }
        boolean wasPlaying = playing;
        loading = false;
        playing = false;
        if (wasPlaying) {
            player.stop();
        }
        notifyState();
        if (listener != null) {
            listener.onTtsError(message);
        }
    }

    static String describe(ApiException e) {
        if ("tts_not_enabled".equals(e.code())) {
            return "语音朗读暂未开通";
        }
        if ("tts_failed".equals(e.code())) {
            return "语音合成服务暂时不可用，请稍后再试";
        }
        String m = e.getMessage();
        return m == null || m.isEmpty() ? "朗读失败，请重试" : m;
    }

    private void notifyState() {
        if (listener != null) {
            listener.onTtsState(key, loading, playing);
        }
    }
}
