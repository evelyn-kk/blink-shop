package com.blink.shop.data;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertTrue;

import java.io.File;
import java.io.IOException;
import java.nio.charset.StandardCharsets;
import java.nio.file.Files;
import java.util.HashSet;
import java.util.Set;

import org.json.JSONObject;

import okhttp3.HttpUrl;
import okhttp3.mockwebserver.MockResponse;
import okhttp3.mockwebserver.RecordedRequest;

/** backend/fixtures/http 下的接口样例（与服务端和 Web 共用）。 */
final class Fixture {

    static final File DIR = new File("../../backend/fixtures/http");

    final String name;
    final JSONObject request;
    final JSONObject response;

    private Fixture(String name, JSONObject root) throws Exception {
        this.name = name;
        request = root.getJSONObject("request");
        response = root.getJSONObject("response");
    }

    private static Set<String> keys(JSONObject o) {
        Set<String> out = new HashSet<>();
        java.util.Iterator<String> it = o.keys();
        while (it.hasNext()) {
            out.add(it.next());
        }
        return out;
    }

    static Fixture load(String name) throws Exception {
        File f = new File(DIR, name + ".json");
        String text = new String(Files.readAllBytes(f.toPath()), StandardCharsets.UTF_8);
        return new Fixture(name, new JSONObject(text));
    }

    /** 样例里的占位符（<token>、<timestamp> 等）换成具体值后作为模拟响应。 */
    MockResponse mockResponse() throws Exception {
        String body = response.has("body") ? response.get("body").toString() : "";
        body = body.replace("<token>", "tok_fixture").replace("<timestamp>", "2026-10-07T00:00:00Z")
                .replace("<request_id>", "rid_fixture").replace("<account_id>", "acct_fixture");
        return new MockResponse().setResponseCode(response.getInt("status")).setHeader("Content-Type", "application/json")
                .setBody(body);
    }

    JSONObject body() throws Exception {
        return response.getJSONObject("body");
    }

    /** 方法、路径与样例一致；查询参数只用样例里出现过的名字（外加分页参数）；JSON 请求体与样例字段一致。 */
    void assertRequest(RecordedRequest actual) throws Exception {
        assertEquals(name + " method", request.getString("method"), actual.getMethod());
        HttpUrl want = HttpUrl.get("http://x" + request.getString("path"));
        HttpUrl got = actual.getRequestUrl();
        assertEquals(name + " path", want.encodedPath(), got.encodedPath());
        Set<String> allowed = new HashSet<>(want.queryParameterNames());
        allowed.add("page");
        allowed.add("page_size");
        for (String q : got.queryParameterNames()) {
            assertTrue(name + " unexpected query " + q, allowed.contains(q));
        }
        for (String q : want.queryParameterNames()) {
            if (!q.equals("page") && !q.equals("page_size")) {
                assertEquals(name + " query " + q, want.queryParameter(q), got.queryParameter(q));
            }
        }
        if (request.has("body")) {
            JSONObject sent = new JSONObject(actual.getBody().readUtf8());
            JSONObject expected = request.getJSONObject("body");
            assertEquals(name + " body keys", keys(expected), keys(sent));
            for (String k : keys(expected)) {
                assertEquals(name + " body " + k, String.valueOf(expected.opt(k)), String.valueOf(sent.opt(k)));
            }
        }
    }
}
