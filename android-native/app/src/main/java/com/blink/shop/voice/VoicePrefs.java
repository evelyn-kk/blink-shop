package com.blink.shop.voice;

import android.content.Context;
import android.content.SharedPreferences;

/** 用户是否开启语音功能（语音输入和朗读按钮）；默认开启，可在设置里关闭。 */
public final class VoicePrefs {

    private static final String FILE = "blink_voice";
    private static final String KEY_ENABLED = "enabled";

    private VoicePrefs() {
    }

    private static SharedPreferences prefs(Context c) {
        return c.getApplicationContext().getSharedPreferences(FILE, Context.MODE_PRIVATE);
    }

    public static boolean enabled(Context c) {
        return prefs(c).getBoolean(KEY_ENABLED, true);
    }

    public static void setEnabled(Context c, boolean on) {
        prefs(c).edit().putBoolean(KEY_ENABLED, on).apply();
    }

    /** 首次申请录音权限前、设置页里展示的隐私说明。 */
    public static final String PRIVACY = "语音输入只用于把你说的话转成文字：录音在你点麦克风后才开始，再点一次、点“取消”、离开聊天页或说满 60 秒就结束；"
            + "声音经 Blink 服务器实时转发给语音识别服务，转写完成后不保存录音和文字原文。朗读时，回答文字经服务器发给语音合成服务，"
            + "生成的音频只在本机临时播放。语音服务的密钥只保存在服务器上。可以随时在“设置与帮助”里关闭语音功能。";
}
