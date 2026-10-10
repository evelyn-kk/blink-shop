package com.blink.shop.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertTrue;
import static org.junit.Assert.fail;

import org.junit.After;
import org.junit.Before;
import org.junit.Test;

import com.blink.shop.model.SpeechConfig;
import com.blink.shop.net.ApiClient;
import com.blink.shop.net.ApiConfig;
import com.blink.shop.net.ApiException;
import com.blink.shop.net.FakeSession;

import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.MockWebServer;
import okhttp3.mockwebserver.RecordedRequest;

/** 语音接口契约：能力配置、朗读（成功返回音频字节、失败按 JSON 错误）。 */
public class SpeechContractTest {

    private MockWebServer server;
    private ShopApi api;

    @Before
    public void setUp() throws Exception {
        server = new MockWebServer();
        server.start();
        api = new ShopApi(new ApiClient(ApiClient.defaultHttp().build(), new ApiConfig(server.url("/api/v1").toString()),
                new FakeSession("tok"), () -> true));
    }

    @After
    public void tearDown() throws Exception {
        server.shutdown();
    }

    @Test
    public void configDisabledAndEnabled() throws Exception {
        Fixture off = Fixture.load("speech_tts_config_disabled");
        server.enqueue(off.mockResponse());
        SpeechConfig c = api.speechConfig();
        off.assertRequest(server.takeRequest());
        assertFalse(c.ttsEnabled);
        assertFalse(c.sttEnabled);
        assertEquals(800, c.maxTextChars);

        Fixture on = Fixture.load("speech_tts_config_enabled");
        server.enqueue(on.mockResponse());
        c = api.speechConfig();
        assertTrue(c.ttsEnabled && c.sttEnabled);
        assertEquals(16000, c.sampleRate);
        assertEquals(60, c.maxSeconds);
    }

    @Test
    public void ttsReturnsAudioBytes() throws Exception {
        byte[] wav = {'R', 'I', 'F', 'F', 0, 1};
        server.enqueue(new MockResponse().setHeader("Content-Type", "audio/wav").setBody(new okio.Buffer().write(wav)));
        ApiClient.Bytes b = api.tts("你好");
        assertEquals("audio/wav", b.contentType);
        assertEquals(6, b.data.length);
        RecordedRequest req = server.takeRequest();
        assertEquals("/api/v1/speech/tts", req.getPath());
        assertEquals("Bearer tok", req.getHeader("Authorization"));
        assertEquals("{\"text\":\"你好\"}", req.getBody().readUtf8());
    }

    @Test
    public void ttsErrorsUseFixtureCodes() throws Exception {
        for (String name : new String[]{"speech_tts_not_enabled", "speech_tts_empty_text"}) {
            Fixture f = Fixture.load(name);
            server.enqueue(f.mockResponse());
            try {
                api.tts("x");
                fail(name);
            } catch (ApiException e) {
                assertEquals(f.response.getJSONObject("body").getString("code"), e.code());
            }
        }
    }
}
