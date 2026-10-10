package com.blink.shop.voice;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.Executor;

import org.junit.Before;
import org.junit.Test;

import com.blink.shop.net.ApiException;

public class TtsControllerTest {

    static final class FakePlayer implements TtsController.Player {
        TtsController.Audio playing;
        Runnable done;
        TtsController.ErrorSink error;
        int stops;
        boolean released;

        @Override
        public void play(TtsController.Audio audio, Runnable d, TtsController.ErrorSink e) {
            playing = audio;
            done = d;
            error = e;
        }

        @Override
        public void stop() {
            stops++;
            playing = null;
        }

        @Override
        public void release() {
            released = true;
        }
    }

    private final List<Runnable> io = new ArrayList<>();
    private final Executor ioExec = io::add;
    private final Executor main = Runnable::run;
    private final List<String> requested = new ArrayList<>();
    private final List<String> log = new ArrayList<>();
    private FakePlayer player;
    private ApiException fail;
    private TtsController c;

    @Before
    public void setUp() {
        player = new FakePlayer();
        c = new TtsController(text -> {
            requested.add(text);
            if (fail != null) {
                throw fail;
            }
            return new TtsController.Audio(new byte[]{1, 2}, "audio/wav");
        }, player, ioExec, main);
        c.setListener(new TtsController.Listener() {
            @Override
            public void onTtsState(String key, boolean loading, boolean playing) {
                log.add(key + (loading ? ":loading" : playing ? ":playing" : ":idle"));
            }

            @Override
            public void onTtsError(String message) {
                log.add("error:" + message);
            }
        });
        c.setMaxChars(10);
    }

    private void runIo() {
        List<Runnable> now = new ArrayList<>(io);
        io.clear();
        now.forEach(Runnable::run);
    }

    @Test
    public void playsCleanTextAndToggles() {
        assertTrue(c.toggle("t1", "**你好**，[看看](https://x) p_seed_mouse 这款"));
        assertTrue(c.isLoading("t1"));
        runIo();
        assertEquals("你好，看看 这款", requested.get(0));
        assertEquals("audio/wav", player.playing.contentType);
        assertTrue(c.isActive("t1"));
        // 再点同一段：停止
        assertFalse(c.toggle("t1", "你好"));
        assertEquals(1, player.stops);
        assertFalse(c.isActive("t1"));
        assertEquals("[t1:loading, t1:playing, t1:idle]", log.toString());
    }

    @Test
    public void switchingDropsLateAudio() {
        c.toggle("t1", "第一段");
        c.toggle("t2", "第二段"); // 第一段还在取音频
        runIo();
        assertEquals(1, requested.stream().filter("第二段"::equals).count());
        assertTrue(c.isActive("t2"));
        assertFalse(c.isActive("t1"));
        player.done.run();
        assertFalse(c.isActive("t2"));
    }

    @Test
    public void stopWhileLoadingAndRelease() {
        c.toggle("t1", "你好");
        c.stop();
        runIo();
        assertEquals(null, player.playing);
        c.release();
        assertTrue(player.released);
    }

    @Test
    public void errors() {
        assertFalse(c.toggle("t1", "https://x.example p_seed_mouse"));
        assertTrue(log.contains("error:这段回答没有可以朗读的文字"));
        fail = ApiException.fromHttp(501, "{\"code\":\"tts_not_enabled\",\"message\":\"x\"}", null);
        c.toggle("t1", "你好");
        runIo();
        assertTrue(log.contains("error:语音朗读暂未开通"));
        assertFalse(c.isActive("t1"));
        fail = null;
        c.toggle("t1", "你好");
        runIo();
        player.error.onError("播放失败");
        assertTrue(log.contains("error:播放失败"));
        assertFalse(c.isActive("t1"));
    }
}
