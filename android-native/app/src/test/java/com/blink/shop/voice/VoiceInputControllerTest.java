package com.blink.shop.voice;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.List;
import java.util.concurrent.Executor;

import org.junit.Before;
import org.junit.Test;

/** 语音输入状态机：假连接、假麦克风，执行器同步。 */
public class VoiceInputControllerTest {

    static final class FakeMic implements VoiceInputController.Mic {
        VoiceInputController.Sink sink;
        boolean running;
        boolean available = true;
        int stops;

        @Override
        public boolean start(VoiceInputController.Sink s) {
            if (!available) {
                return false;
            }
            sink = s;
            running = true;
            return true;
        }

        @Override
        public void stop() {
            running = false;
            stops++;
        }
    }

    static final class FakeSocket implements VoiceInputController.Socket {
        final List<String> texts = new ArrayList<>();
        int binaryBytes;
        boolean cancelled;

        @Override
        public boolean sendBinary(byte[] data) {
            binaryBytes += data.length;
            return true;
        }

        @Override
        public boolean sendText(String text) {
            texts.add(text);
            return true;
        }

        @Override
        public void cancel() {
            cancelled = true;
        }
    }

    private final Executor direct = Runnable::run;
    private FakeMic mic;
    private FakeSocket socket;
    private VoiceInputController.SocketListener server;
    private VoiceInputController c;
    private final List<String> log = new ArrayList<>();

    @Before
    public void setUp() {
        mic = new FakeMic();
        socket = new FakeSocket();
        c = new VoiceInputController(l -> {
            server = l;
            return socket;
        }, mic, direct);
        c.setListener(new VoiceInputController.Listener() {
            @Override
            public void onVoiceState(VoiceInputController.State state) {
                log.add("state:" + state);
            }

            @Override
            public void onTranscript(String text, boolean isFinal) {
                log.add((isFinal ? "final:" : "partial:") + text);
            }

            @Override
            public void onVoiceError(String message) {
                log.add("error:" + message);
            }
        });
    }

    @Test
    public void fullSession() {
        assertTrue(c.start());
        assertFalse("already active", c.start());
        assertFalse("mic waits for ready", mic.running);
        server.onMessage("{\"type\":\"ready\",\"sample_rate\":16000}");
        assertTrue(mic.running);
        mic.sink.onPcm(new byte[3200]);
        mic.sink.onPcm(new byte[3200]);
        assertEquals(6400, socket.binaryBytes);
        server.onMessage("{\"type\":\"partial\",\"text\":\"推荐\",\"seq\":1}");
        c.finish();
        assertFalse(mic.running);
        assertEquals("{\"type\":\"end\"}", socket.texts.get(0));
        mic.sink.onPcm(new byte[3200]); // 停止后迟到的音频不再发送
        assertEquals(6400, socket.binaryBytes);
        server.onMessage("{\"type\":\"final\",\"text\":\"推荐一款耳机\",\"seq\":2}");
        server.onMessage("{\"type\":\"closed\"}");
        assertEquals(VoiceInputController.State.IDLE, c.state());
        assertEquals("[state:CONNECTING, state:LISTENING, partial:推荐, state:FINISHING, final:推荐一款耳机, state:IDLE]", log.toString());
        assertTrue(socket.cancelled);
    }

    @Test
    public void serverEndsAtMaxDuration() {
        c.start();
        server.onMessage("{\"type\":\"ready\"}");
        server.onMessage("{\"type\":\"end_of_input\",\"reason\":\"max_duration\"}");
        assertFalse(mic.running);
        assertEquals(VoiceInputController.State.FINISHING, c.state());
        server.onMessage("{\"type\":\"final\",\"text\":\"好\"}");
        server.onMessage("{\"type\":\"closed\"}");
        assertTrue(log.contains("final:好"));
        assertFalse(log.toString().contains("error"));
    }

    @Test
    public void cancelIgnoresLateEvents() {
        c.start();
        server.onMessage("{\"type\":\"ready\"}");
        c.cancel();
        assertFalse(mic.running);
        assertTrue(socket.cancelled);
        assertTrue(socket.texts.contains("{\"type\":\"cancel\"}"));
        server.onMessage("{\"type\":\"final\",\"text\":\"迟到\"}");
        server.onFailure(0, "eof");
        assertFalse(log.toString().contains("迟到"));
        assertFalse(log.toString().contains("error"));
        assertEquals(VoiceInputController.State.IDLE, c.state());
    }

    @Test
    public void finishWhileConnectingCancels() {
        c.start();
        c.finish();
        assertEquals(VoiceInputController.State.IDLE, c.state());
        assertTrue(socket.cancelled);
    }

    @Test
    public void errorsAreExplained() {
        c.start();
        server.onFailure(501, "not enabled");
        assertTrue(log.contains("error:语音输入暂未开通，请用文字提问"));

        log.clear();
        c.start();
        server.onMessage("{\"type\":\"ready\"}");
        server.onFailure(0, "socket reset");
        assertTrue(log.contains("error:网络中断，语音输入已停止"));
        assertFalse(mic.running);

        log.clear();
        c.start();
        server.onMessage("{\"type\":\"ready\"}");
        server.onMessage("{\"type\":\"error\",\"code\":\"idle_timeout\",\"message\":\"x\"}");
        server.onMessage("{\"type\":\"closed\"}");
        assertEquals("[state:CONNECTING, state:LISTENING, state:IDLE, error:很久没听到声音，已停止语音输入]", log.toString());

        log.clear();
        mic.available = false;
        c.start();
        server.onMessage("{\"type\":\"ready\"}");
        assertTrue(log.contains("error:麦克风不可用，请检查是否被其他应用占用"));
        assertEquals(VoiceInputController.State.IDLE, c.state());

        // 没收到最终结果就断开
        log.clear();
        mic.available = true;
        c.start();
        server.onMessage("{\"type\":\"ready\"}");
        server.onMessage("{\"type\":\"closed\"}");
        assertTrue(log.contains("error:语音输入意外结束，请重试"));
    }

    @Test
    public void micFailureStops() {
        c.start();
        server.onMessage("{\"type\":\"ready\"}");
        mic.sink.onMicError("录音被系统中断");
        assertTrue(log.contains("error:录音被系统中断"));
        assertTrue(socket.cancelled);
    }
}
