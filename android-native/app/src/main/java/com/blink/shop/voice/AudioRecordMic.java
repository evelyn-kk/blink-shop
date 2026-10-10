package com.blink.shop.voice;

import android.annotation.SuppressLint;
import android.media.AudioFormat;
import android.media.AudioRecord;
import android.media.MediaRecorder;

/**
 * 用 AudioRecord 录 16kHz 单声道 16 位 PCM，每 100ms（3200 字节）回调一块。调用方负责先拿到录音权限。
 * stop 后录音线程退出并释放 AudioRecord；可以重复调用。
 */
public final class AudioRecordMic implements VoiceInputController.Mic {

    static final int SAMPLE_RATE = 16000;
    static final int CHUNK_BYTES = SAMPLE_RATE * 2 / 10;

    private AudioRecord record;
    private Thread thread;
    private volatile boolean running;

    @SuppressLint("MissingPermission") // 权限由 ChatActivity 在开始前检查
    @Override
    public synchronized boolean start(VoiceInputController.Sink sink) {
        stop();
        int min = AudioRecord.getMinBufferSize(SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO, AudioFormat.ENCODING_PCM_16BIT);
        if (min <= 0) {
            return false;
        }
        AudioRecord r;
        try {
            r = new AudioRecord(MediaRecorder.AudioSource.VOICE_RECOGNITION, SAMPLE_RATE, AudioFormat.CHANNEL_IN_MONO,
                    AudioFormat.ENCODING_PCM_16BIT, Math.max(min, CHUNK_BYTES * 4));
        } catch (RuntimeException e) {
            return false;
        }
        if (r.getState() != AudioRecord.STATE_INITIALIZED) {
            r.release();
            return false;
        }
        try {
            r.startRecording();
        } catch (IllegalStateException e) {
            r.release();
            return false;
        }
        if (r.getRecordingState() != AudioRecord.RECORDSTATE_RECORDING) {
            r.release();
            return false;
        }
        record = r;
        running = true;
        thread = new Thread(() -> {
            byte[] buf = new byte[CHUNK_BYTES];
            while (running) {
                int n = r.read(buf, 0, buf.length);
                if (n < 0) {
                    if (running) {
                        sink.onMicError("录音被系统中断，请重试");
                    }
                    return;
                }
                if (n > 0 && running) {
                    byte[] chunk = new byte[n];
                    System.arraycopy(buf, 0, chunk, 0, n);
                    sink.onPcm(chunk);
                }
            }
        }, "blink-mic");
        thread.start();
        return true;
    }

    @Override
    public synchronized void stop() {
        running = false;
        AudioRecord r = record;
        record = null;
        if (r != null) {
            try {
                r.stop();
            } catch (IllegalStateException ignored) {
                // 已停止
            }
        }
        Thread t = thread;
        thread = null;
        if (t != null) {
            try {
                t.join(500);
            } catch (InterruptedException e) {
                Thread.currentThread().interrupt();
            }
        }
        if (r != null) {
            r.release();
        }
    }
}
