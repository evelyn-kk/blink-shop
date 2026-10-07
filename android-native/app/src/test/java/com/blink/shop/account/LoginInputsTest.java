package com.blink.shop.account;

import static org.junit.Assert.assertEquals;
import static org.junit.Assert.assertFalse;
import static org.junit.Assert.assertNotNull;
import static org.junit.Assert.assertTrue;

import java.io.File;
import java.util.HashMap;
import java.util.Map;

import javax.xml.parsers.DocumentBuilderFactory;

import org.junit.Test;
import org.w3c.dom.Element;
import org.w3c.dom.NodeList;

import android.view.inputmethod.EditorInfo;

public class LoginInputsTest {

    private static final String ANDROID_NS = "http://schemas.android.com/apk/res/android";

    @Test
    public void usernameForcesAsciiKeyboard() {
        int o = LoginInputs.usernameImeOptions();
        assertTrue((o & EditorInfo.IME_FLAG_FORCE_ASCII) != 0);
        assertEquals(EditorInfo.IME_ACTION_NEXT, o & EditorInfo.IME_MASK_ACTION);
    }

    @Test
    public void passwordAllowsAnyKeyboard() {
        // 服务端允许中文等多字节密码，不能要求英文键盘
        for (boolean register : new boolean[] {false, true}) {
            int o = LoginInputs.passwordImeOptions(register);
            assertEquals(0, o & EditorInfo.IME_FLAG_FORCE_ASCII);
            assertEquals(register ? EditorInfo.IME_ACTION_NEXT : EditorInfo.IME_ACTION_DONE, o & EditorInfo.IME_MASK_ACTION);
        }
    }

    /** 读取布局里各输入框（按 id）的属性。 */
    private static Map<String, Element> editTexts(String layout) throws Exception {
        File f = new File("src/main/res/layout/" + layout + ".xml");
        DocumentBuilderFactory dbf = DocumentBuilderFactory.newInstance();
        dbf.setNamespaceAware(true);
        NodeList list = dbf.newDocumentBuilder().parse(f).getElementsByTagName("EditText");
        Map<String, Element> out = new HashMap<>();
        for (int i = 0; i < list.getLength(); i++) {
            Element e = (Element) list.item(i);
            out.put(e.getAttributeNS(ANDROID_NS, "id").replace("@+id/", "").replace("@id/", ""), e);
        }
        return out;
    }

    @Test
    public void layoutsDoNotRestrictPasswordKeyboard() throws Exception {
        Map<String, Element> login = editTexts("activity_login");
        Element password = login.get("password");
        assertNotNull(password);
        assertEquals("textPassword", password.getAttributeNS(ANDROID_NS, "inputType"));
        // 布局不写 imeOptions，由 LoginInputs 统一设置，避免两处不一致
        assertFalse(password.hasAttributeNS(ANDROID_NS, "imeOptions"));
        assertFalse(login.get("username").hasAttributeNS(ANDROID_NS, "imeOptions"));
        // 接口地址只会是英文
        Element api = editTexts("activity_api_settings").get("api_base_input");
        assertTrue(api.getAttributeNS(ANDROID_NS, "imeOptions").contains("flagForceAscii"));
    }
}
