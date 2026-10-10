package com.blink.shop.chat;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNull;
import static org.junit.Assert.assertTrue;

import java.util.ArrayList;
import java.util.Arrays;
import java.util.Collections;
import java.util.List;
import java.util.concurrent.Executor;

import org.junit.Before;
import org.junit.Test;

import com.blink.shop.net.ApiException;

/** 附图上传状态：io 执行器手动推进，便于检查换图、移除时旧结果被丢弃。 */
public class AttachmentControllerTest {

    private final List<Runnable> pending = new ArrayList<>();
    private final List<byte[]> uploaded = new ArrayList<>();
    private final Executor io = pending::add;
    private final Executor main = Runnable::run;
    private ApiException failWith;
    private int changes;
    private AttachmentController a;

    @Before
    public void setUp() {
        a = new AttachmentController(jpeg -> {
            uploaded.add(jpeg);
            if (failWith != null) {
                throw failWith;
            }
            return "file_" + uploaded.size();
        }, io, main);
        a.setListener(() -> changes++);
    }

    private void runIo() {
        List<Runnable> now = new ArrayList<>(pending);
        pending.clear();
        now.forEach(Runnable::run);
    }

    @Test
    public void uploadsAndBecomesReady() {
        assertEquals(AttachmentController.State.EMPTY, a.state());
        assertTrue(a.readyIds().isEmpty());
        a.attach(new byte[]{1, 2, 3});
        assertTrue(a.isUploading());
        assertTrue("上传完成前不能随消息发出", a.readyIds().isEmpty());
        runIo();
        assertEquals(AttachmentController.State.READY, a.state());
        assertEquals(Collections.singletonList("file_1"), a.readyIds());
        a.consumed();
        assertEquals(AttachmentController.State.EMPTY, a.state());
        assertNull(a.data());
        assertTrue(changes >= 3);
    }

    @Test
    public void replacingOrRemovingDropsStaleUpload() {
        a.attach(new byte[]{1});
        a.attach(new byte[]{2}); // 换了一张
        runIo();
        assertEquals(Collections.singletonList("file_2"), a.readyIds());
        a.attach(new byte[]{3});
        a.clear(); // 上传中移除
        runIo();
        assertEquals(AttachmentController.State.EMPTY, a.state());
        assertTrue(a.readyIds().isEmpty());
    }

    @Test
    public void failureCanBeRetried() {
        failWith = ApiException.fromHttp(400, "{\"code\":\"unsupported_file_type\",\"message\":\"不支持\",\"field\":\"file\"}", null);
        a.attach(new byte[]{9});
        runIo();
        assertEquals(AttachmentController.State.FAILED, a.state());
        assertTrue(a.error().contains("格式不支持"));
        failWith = null;
        assertTrue(a.retry());
        runIo();
        assertEquals(AttachmentController.State.READY, a.state());
        assertTrue(Arrays.equals(new byte[]{9}, uploaded.get(1)));
    }

    @Test
    public void localFailureHasNothingToRetry() {
        a.fail("这张图片打不开，请换一张");
        assertEquals(AttachmentController.State.FAILED, a.state());
        assertFalse(a.retry());
        a.attach(new byte[0]);
        assertEquals(AttachmentController.State.FAILED, a.state());
        assertTrue(pending.isEmpty());
        assertEquals("图片太大了，请换一张小一点的",
                AttachmentController.describe(ApiException.fromHttp(413, "{\"code\":\"payload_too_large\",\"message\":\"x\"}", null)));
    }
}
