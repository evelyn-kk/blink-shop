package com.blink.shop.model;

import org.json.JSONObject;

/** 服务端的语音能力配置（GET /speech/tts/config）。 */
public final class SpeechConfig {
    public final boolean ttsEnabled;
    public final int maxTextChars;
    public final boolean sttEnabled;
    public final int sampleRate;
    public final int maxSeconds;

    public SpeechConfig(boolean ttsEnabled, int maxTextChars, boolean sttEnabled, int sampleRate, int maxSeconds) {
        this.ttsEnabled = ttsEnabled;
        this.maxTextChars = maxTextChars;
        this.sttEnabled = sttEnabled;
        this.sampleRate = sampleRate;
        this.maxSeconds = maxSeconds;
    }

    /** 读取失败时按“都没开通”处理。 */
    public static final SpeechConfig DISABLED = new SpeechConfig(false, 0, false, 16000, 0);

    public static SpeechConfig fromJson(JSONObject o) {
        JSONObject stt = o.optJSONObject("stt");
        return new SpeechConfig(o.optBoolean("enabled", false), o.optInt("max_text_chars", 800),
                stt != null && stt.optBoolean("enabled", false), stt == null ? 16000 : stt.optInt("sample_rate", 16000),
                stt == null ? 60 : stt.optInt("max_seconds", 60));
    }
}
