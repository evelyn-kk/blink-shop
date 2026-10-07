package com.blink.shop.account;

import android.view.inputmethod.EditorInfo;

/**
 * 登录页输入框的输入法设置。账号只允许英文字母、数字、下划线和短横线，要求英文键盘，
 * 避免中文输入法把拼写转成汉字；密码按服务端规则可以包含中文等任意字符，不限制键盘。
 */
final class LoginInputs {

    private LoginInputs() {
    }

    static int usernameImeOptions() {
        return EditorInfo.IME_ACTION_NEXT | EditorInfo.IME_FLAG_FORCE_ASCII;
    }

    /** 注册时密码后面还有昵称（下一项），登录时直接提交。 */
    static int passwordImeOptions(boolean register) {
        return register ? EditorInfo.IME_ACTION_NEXT : EditorInfo.IME_ACTION_DONE;
    }
}
