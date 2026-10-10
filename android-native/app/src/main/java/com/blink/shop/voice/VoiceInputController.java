package com.blink.shop.voice;

import java.util.concurrent.Executor;

import org.json.JSONException;
import org.json.JSONObject;

/**
 * 语音输入：连上服务端的实时识别（WebSocket）→ 收到 ready 后开始录音，PCM 按块发出 → 用户再点一次结束（发 end）→
 * 收到 final 后把文字交给输入框。纯 Java，不依赖 Android；录音和连接通过接口注入，回调都切回 main 执行器处理。
 * 用户取消、页面退出、出错时都会停掉录音并关闭连接，迟到的回调按 generation 丢弃。
 */
public final class VoiceInputController {

    public enum State {
        IDLE,
        /** 正在连接服务端。 */
        CONNECTING,
        /** 正在录音、实时转写。 */
        LISTENING,
        /** 已结束录音，等最终结果。 */
        FINISHING
    }

    /** 录音来源：start 后在任意线程回调 PCM 块（16kHz 单声道 16 位）。 */
    public interface Mic {
        /** 返回 false 表示麦克风不可用（被占用、没有权限）。 */
        boolean start(Sink sink);

        void stop();
    }

    public interface Sink {
        void onPcm(byte[] chunk);

        void onMicError(String message);
    }

    /** 到服务端的连接。 */
    public interface Socket {
        boolean sendBinary(byte[] data);

        boolean sendText(String text);

        /** 立即断开（不等服务端）。 */
        void cancel();
    }

    public interface SocketListener {
        void onMessage(String text);

        /** 连接失败或中断；code 为 HTTP 错误码（握手被拒时，如 501、409），没有时为 0。 */
        void onFailure(int httpCode, String message);

        void onClosed();
    }

    public interface Connector {
        Socket open(SocketListener listener);
    }

    public interface Listener {
        void onVoiceState(State state);

        /** 转写文字更新；isFinal 为 true 时是最终结果（可能为空：没听到内容）。 */
        void onTranscript(String text, boolean isFinal);

        void onVoiceError(String message);
    }

    private final Connector connector;
    private final Mic mic;
    private final Executor main;
    private Listener listener;

    private State state = State.IDLE;
    private Socket socket;
    private int generation;
    private boolean gotFinal;

    public VoiceInputController(Connector connector, Mic mic, Executor main) {
        this.connector = connector;
        this.mic = mic;
        this.main = main;
    }

    public void setListener(Listener l) {
        listener = l;
    }

    public State state() {
        return state;
    }

    public boolean isActive() {
        return state != State.IDLE;
    }

    /** 开始语音输入（已在进行中时不做任何事，返回 false）。 */
    public boolean start() {
        if (state != State.IDLE) {
            return false;
        }
        generation++;
        final int gen = generation;
        gotFinal = false;
        setState(State.CONNECTING);
        socket = connector.open(new SocketListener() {
            @Override
            public void onMessage(String text) {
                main.execute(() -> handleMessage(gen, text));
            }

            @Override
            public void onFailure(int httpCode, String message) {
                main.execute(() -> handleFailure(gen, httpCode, message));
            }

            @Override
            public void onClosed() {
                main.execute(() -> handleClosed(gen));
            }
        });
        if (socket == null) {
            fail("语音输入暂时不可用，请用文字提问");
            return false;
        }
        return true;
    }

    /** 说完了：停止录音，等最终结果。 */
    public void finish() {
        if (state == State.CONNECTING) {
            cancel(); // 还没开始录音：直接取消
            return;
        }
        if (state != State.LISTENING) {
            return;
        }
        mic.stop();
        setState(State.FINISHING);
        socket.sendText("{\"type\":\"end\"}");
    }

    /** 放弃这次输入（页面退出、用户取消）：停录音、断连接，不再回调结果。 */
    public void cancel() {
        if (state == State.IDLE) {
            return;
        }
        generation++;
        mic.stop();
        Socket s = socket;
        socket = null;
        if (s != null) {
            s.sendText("{\"type\":\"cancel\"}");
            s.cancel();
        }
        setState(State.IDLE);
    }

    private void handleMessage(int gen, String text) {
        if (gen != generation) {
            return;
        }
        JSONObject ev;
        try {
            ev = new JSONObject(text);
        } catch (JSONException e) {
            return;
        }
        switch (ev.optString("type")) {
            case "ready":
                if (state != State.CONNECTING) {
                    return;
                }
                boolean ok = mic.start(new Sink() {
                    @Override
                    public void onPcm(byte[] chunk) {
                        main.execute(() -> {
                            if (gen == generation && state == State.LISTENING && socket != null) {
                                socket.sendBinary(chunk);
                            }
                        });
                    }

                    @Override
                    public void onMicError(String message) {
                        main.execute(() -> {
                            if (gen == generation) {
                                fail(message);
                            }
                        });
                    }
                });
                if (!ok) {
                    fail("麦克风不可用，请检查是否被其他应用占用");
                    return;
                }
                setState(State.LISTENING);
                break;
            case "partial":
                if (listener != null) {
                    listener.onTranscript(ev.optString("text"), false);
                }
                break;
            case "end_of_input":
                // 服务端到了时长上限：停止录音，等最终结果
                if (state == State.LISTENING) {
                    mic.stop();
                    setState(State.FINISHING);
                }
                break;
            case "final":
                gotFinal = true;
                if (listener != null) {
                    listener.onTranscript(ev.optString("text"), true);
                }
                break;
            case "error":
                fail(errorText(ev.optString("code"), ev.optString("message")));
                break;
            case "closed":
                handleClosed(gen);
                break;
            default:
                break;
        }
    }

    private void handleFailure(int gen, int httpCode, String message) {
        if (gen != generation) {
            return;
        }
        if (httpCode == 501) {
            fail("语音输入暂未开通，请用文字提问");
        } else if (httpCode == 409) {
            fail("已有一段语音输入正在进行，请稍后再试");
        } else if (httpCode == 401) {
            fail("登录已失效，请重新登录");
        } else {
            fail(state == State.CONNECTING ? "连不上语音服务，请检查网络后重试" : "网络中断，语音输入已停止");
        }
    }

    private void handleClosed(int gen) {
        if (gen != generation || state == State.IDLE) {
            return;
        }
        boolean expected = gotFinal;
        generation++;
        mic.stop();
        Socket s = socket;
        socket = null;
        if (s != null) {
            s.cancel();
        }
        setState(State.IDLE);
        if (!expected && listener != null) {
            listener.onVoiceError("语音输入意外结束，请重试");
        }
    }

    private void fail(String message) {
        generation++;
        mic.stop();
        Socket s = socket;
        socket = null;
        if (s != null) {
            s.cancel();
        }
        setState(State.IDLE);
        if (listener != null) {
            listener.onVoiceError(message);
        }
    }

    static String errorText(String code, String message) {
        switch (code) {
            case "idle_timeout":
                return "很久没听到声音，已停止语音输入";
            case "speech_unavailable":
                return "语音识别服务暂时不可用，请稍后再试或用文字提问";
            case "speech_timeout":
                return "语音输入超时了，请重试";
            default:
                return message == null || message.isEmpty() ? "语音识别出错了，请重试" : message;
        }
    }

    private void setState(State s) {
        if (state == s) {
            return;
        }
        state = s;
        if (listener != null) {
            listener.onVoiceState(s);
        }
    }
}
