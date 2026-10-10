package com.blink.shop.chat;

import java.util.Collections;
import java.util.List;
import java.util.concurrent.Executor;

import com.blink.shop.net.ApiException;

/**
 * 聊天附图：选好的图片（已压缩成 JPEG）立即上传，拿到 file_id 后随下一条消息发出。一次只带一张。纯 Java，不依赖 Android。
 * 状态只在主线程修改；上传在 io 执行器里跑，结果回到 main。换图、移除后，旧的上传结果被丢弃。
 */
public final class AttachmentController {

    public enum State {
        EMPTY,
        UPLOADING,
        READY,
        /** 上传失败或图片处理失败；error() 说明原因，可以 retry()（有图片数据时）或移除。 */
        FAILED
    }

    public interface Uploader {
        String upload(byte[] jpeg) throws ApiException;
    }

    public interface Listener {
        void onAttachmentChanged();
    }

    private final Uploader uploader;
    private final Executor io;
    private final Executor main;
    private Listener listener;

    private State state = State.EMPTY;
    private byte[] data;
    private String fileId = "";
    private String error = "";
    private int generation;

    public AttachmentController(Uploader uploader, Executor io, Executor main) {
        this.uploader = uploader;
        this.io = io;
        this.main = main;
    }

    public void setListener(Listener l) {
        listener = l;
    }

    public State state() {
        return state;
    }

    /** 待发送图片的字节（预览用）；没有时为 null。 */
    public byte[] data() {
        return data;
    }

    public String error() {
        return error;
    }

    public boolean isUploading() {
        return state == State.UPLOADING;
    }

    /** 已上传、可以随消息发出的 file_id 列表（没有时为空）。 */
    public List<String> readyIds() {
        return state == State.READY ? Collections.singletonList(fileId) : Collections.emptyList();
    }

    /** 选了一张新图片：替换掉之前的，开始上传。 */
    public void attach(byte[] jpeg) {
        if (jpeg == null || jpeg.length == 0) {
            fail("图片是空的，请换一张");
            return;
        }
        data = jpeg;
        upload();
    }

    /** 图片在本地就处理失败（打不开、不是图片）。 */
    public void fail(String message) {
        generation++;
        data = null;
        fileId = "";
        state = State.FAILED;
        error = message;
        changed();
    }

    /** 上传失败后重试。 */
    public boolean retry() {
        if (state != State.FAILED || data == null) {
            return false;
        }
        upload();
        return true;
    }

    /** 移除待发送的图片（上传中的结果会被丢弃）。 */
    public void clear() {
        generation++;
        state = State.EMPTY;
        data = null;
        fileId = "";
        error = "";
        changed();
    }

    /** 消息已发出：附件交给那一轮，输入区清空。 */
    public void consumed() {
        clear();
    }

    private void upload() {
        generation++;
        final int gen = generation;
        final byte[] bytes = data;
        state = State.UPLOADING;
        fileId = "";
        error = "";
        changed();
        io.execute(() -> {
            try {
                String id = uploader.upload(bytes);
                main.execute(() -> {
                    if (gen != generation) {
                        return;
                    }
                    fileId = id;
                    state = State.READY;
                    changed();
                });
            } catch (ApiException e) {
                main.execute(() -> {
                    if (gen != generation) {
                        return;
                    }
                    state = State.FAILED;
                    error = describe(e);
                    changed();
                });
            }
        });
    }

    static String describe(ApiException e) {
        String code = e.code();
        if ("unsupported_file_type".equals(code)) {
            return "这张图片的格式不支持，请换一张 JPG 或 PNG 图片";
        }
        if ("payload_too_large".equals(code)) {
            return "图片太大了，请换一张小一点的";
        }
        if ("object_storage_unavailable".equals(code)) {
            return "图片上传暂不可用，可以先用文字描述商品";
        }
        String msg = e.getMessage();
        return msg == null || msg.isEmpty() ? "图片上传失败，请重试" : msg;
    }

    private void changed() {
        if (listener != null) {
            listener.onAttachmentChanged();
        }
    }
}
