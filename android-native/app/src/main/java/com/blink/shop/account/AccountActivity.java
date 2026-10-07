package com.blink.shop.account;

import android.app.AlertDialog;
import android.content.Intent;
import android.net.Uri;
import android.os.Bundle;
import android.text.InputFilter;
import android.text.InputType;
import android.view.View;
import android.widget.Button;
import android.widget.EditText;
import android.widget.ImageView;
import android.widget.LinearLayout;
import android.widget.TextView;

import java.io.IOException;

import com.blink.shop.R;
import com.blink.shop.data.ApiBaseStore;
import com.blink.shop.model.Account;
import com.blink.shop.model.Session;
import com.blink.shop.net.ApiException;
import com.blink.shop.settings.ApiSettingsActivity;
import com.blink.shop.ui.Async;
import com.blink.shop.ui.BaseActivity;

/** 账户资料：头像、昵称、联系方式，退出登录和注销。进入时向服务端刷新一次资料。 */
public final class AccountActivity extends BaseActivity {

    private static final int REQUEST_AVATAR = 1;

    private boolean avatarUploading;

    @Override
    protected boolean requiresLogin() {
        return true;
    }

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        if (isFinishing()) {
            return;
        }
        setContentView(R.layout.activity_account);
        findViewById(R.id.back_button).setOnClickListener(v -> finish());
        findViewById(R.id.avatar_button).setOnClickListener(v -> pickAvatar());
        findViewById(R.id.row_nickname).setOnClickListener(v -> editNickname());
        findViewById(R.id.row_phone).setOnClickListener(v -> editContact());
        findViewById(R.id.row_email).setOnClickListener(v -> editContact());
        findViewById(R.id.row_logout).setOnClickListener(v -> confirmLogout());
        findViewById(R.id.row_delete).setOnClickListener(v -> confirmDelete());
        View apiRow = findViewById(R.id.row_api_settings);
        if (ApiBaseStore.editable()) {
            apiRow.setOnClickListener(v -> startActivity(new Intent(this, ApiSettingsActivity.class)));
        } else {
            apiRow.setVisibility(View.GONE);
        }
        render(app.sessions().current());
        refresh();
    }

    @Override
    protected void onSessionUpdated(Session session) {
        render(session);
    }

    private void refresh() {
        Session s = app.sessions().current();
        if (s == null) {
            return;
        }
        call(() -> app.api().me(), new Async.Callback<Account>() {
            @Override
            public void onSuccess(Account account) {
                app.sessions().updateAccount(s.token, account);
            }

            @Override
            public void onError(ApiException e) {
                // 401 由会话监听处理；其他错误继续显示本地资料
                if (!e.isUnauthorized()) {
                    toast("资料刷新失败：" + e.getMessage());
                }
            }
        });
    }

    private void render(Session s) {
        if (s == null) {
            return;
        }
        Account a = s.account;
        ((TextView) findViewById(R.id.account_name)).setText(a.name());
        ((TextView) findViewById(R.id.account_username)).setText("账号：" + a.username);
        TextView initial = findViewById(R.id.avatar_initial);
        initial.setText(a.name().isEmpty() ? "我" : a.name().substring(0, 1));
        ImageView avatar = findViewById(R.id.avatar);
        app.images().load(avatar, a.avatarUrl, dp(72));
        ((TextView) findViewById(R.id.value_nickname)).setText(a.displayName.isEmpty() ? "未设置" : a.displayName);
        ((TextView) findViewById(R.id.value_phone)).setText(a.phone.isEmpty() ? "未设置" : Masking.phone(a.phone));
        ((TextView) findViewById(R.id.value_email)).setText(a.email.isEmpty() ? "未设置" : Masking.email(a.email));
        TextView status = findViewById(R.id.account_status);
        String hint = a.statusHint();
        status.setText(hint);
        status.setVisibility(hint.isEmpty() ? View.GONE : View.VISIBLE);
    }

    // ---------- 昵称 ----------

    private void editNickname() {
        Session s = app.sessions().current();
        if (s == null) {
            return;
        }
        EditText input = dialogField("昵称", s.account.displayName, InputType.TYPE_CLASS_TEXT, 24);
        LinearLayout box = dialogBox(input);
        TextView error = dialogError(box);
        AlertDialog d = new AlertDialog.Builder(this).setTitle("修改昵称").setView(box)
                .setNegativeButton("取消", null).setPositiveButton("保存", null).create();
        d.setOnShowListener(x -> {
            Button save = d.getButton(AlertDialog.BUTTON_POSITIVE);
            save.setOnClickListener(v -> {
                String name = input.getText().toString().trim();
                if (name.isEmpty()) {
                    showDialogError(error, input, "请输入昵称");
                    return;
                }
                save.setEnabled(false);
                call(() -> app.api().updateProfile(name, null), new Async.Callback<Account>() {
                    @Override
                    public void onSuccess(Account account) {
                        app.sessions().updateAccount(s.token, account);
                        d.dismiss();
                        toast("昵称已更新");
                    }

                    @Override
                    public void onError(ApiException e) {
                        save.setEnabled(true);
                        showDialogError(error, input, e.getMessage());
                    }
                });
            });
        });
        d.show();
    }

    // ---------- 联系方式 ----------

    private void editContact() {
        Session s = app.sessions().current();
        if (s == null) {
            return;
        }
        EditText phone = dialogField("手机号（可留空）", s.account.phone, InputType.TYPE_CLASS_PHONE, 32);
        EditText email = dialogField("邮箱（可留空）", s.account.email,
                InputType.TYPE_CLASS_TEXT | InputType.TYPE_TEXT_VARIATION_EMAIL_ADDRESS, 128);
        LinearLayout box = dialogBox(phone, email);
        TextView error = dialogError(box);
        AlertDialog d = new AlertDialog.Builder(this).setTitle("修改联系方式").setView(box)
                .setNegativeButton("取消", null).setPositiveButton("保存", null).create();
        d.setOnShowListener(x -> {
            Button save = d.getButton(AlertDialog.BUTTON_POSITIVE);
            save.setOnClickListener(v -> {
                String p = phone.getText().toString().trim();
                String e = email.getText().toString().trim();
                save.setEnabled(false);
                call(() -> app.api().updateContact(p, e), new Async.Callback<Account>() {
                    @Override
                    public void onSuccess(Account account) {
                        app.sessions().updateAccount(s.token, account);
                        d.dismiss();
                        toast("联系方式已更新");
                    }

                    @Override
                    public void onError(ApiException ex) {
                        save.setEnabled(true);
                        showDialogError(error, "email".equals(ex.field()) ? email : phone, ex.getMessage());
                    }
                });
            });
        });
        d.show();
    }

    // ---------- 头像 ----------

    private void pickAvatar() {
        if (avatarUploading) {
            return;
        }
        Intent pick = new Intent(Intent.ACTION_GET_CONTENT).setType("image/*").addCategory(Intent.CATEGORY_OPENABLE);
        try {
            startActivityForResult(Intent.createChooser(pick, "选择头像"), REQUEST_AVATAR);
        } catch (RuntimeException e) {
            toast("没有可用的图片选择应用");
        }
    }

    @Override
    protected void onActivityResult(int requestCode, int resultCode, Intent data) {
        super.onActivityResult(requestCode, resultCode, data);
        if (requestCode != REQUEST_AVATAR || resultCode != RESULT_OK || data == null || data.getData() == null) {
            return;
        }
        uploadAvatar(data.getData());
    }

    private void uploadAvatar(Uri uri) {
        Session s = app.sessions().current();
        if (s == null) {
            return;
        }
        setAvatarUploading(true);
        call(() -> {
            byte[] jpeg;
            try {
                jpeg = AvatarImage.prepare(getContentResolver(), uri);
            } catch (IOException | RuntimeException e) {
                throw ApiException.local("无法读取这张图片，请换一张", e);
            }
            String url = app.api().uploadAvatar(jpeg, "image/jpeg", "avatar.jpg");
            return app.api().updateProfile(null, url);
        }, new Async.Callback<Account>() {
            @Override
            public void onSuccess(Account account) {
                setAvatarUploading(false);
                app.sessions().updateAccount(s.token, account);
                toast("头像已更新");
            }

            @Override
            public void onError(ApiException e) {
                setAvatarUploading(false);
                toast(e.kind() == ApiException.Kind.LOCAL ? e.getMessage() : "头像上传失败：" + e.getMessage());
            }
        });
    }

    private void setAvatarUploading(boolean on) {
        avatarUploading = on;
        findViewById(R.id.avatar_progress).setVisibility(on ? View.VISIBLE : View.GONE);
    }

    // ---------- 退出与注销 ----------

    private void confirmLogout() {
        new AlertDialog.Builder(this).setTitle("退出登录").setMessage("确定退出当前账号吗？")
                .setNegativeButton("取消", null)
                .setPositiveButton("退出", (d, w) -> logout())
                .show();
    }

    private void logout() {
        // 先在本地退出；服务端撤销 token 失败（如离线）不影响本地退出
        String token = app.sessions().token();
        if (token != null) {
            Async.run(() -> {
                app.api().logout(token);
                return null;
            }, new Async.Callback<Object>() {
                @Override
                public void onSuccess(Object value) {
                }

                @Override
                public void onError(ApiException e) {
                }
            });
        }
        app.sessions().signOut();
        toast("已退出登录");
        finish();
    }

    private void confirmDelete() {
        new AlertDialog.Builder(this).setTitle("注销账户")
                .setMessage("注销后账号无法再登录，用户名也不能再注册。确定注销吗？")
                .setNegativeButton("取消", null)
                .setPositiveButton("注销", (d, w) -> deleteAccount())
                .show();
    }

    private void deleteAccount() {
        call(() -> {
            app.api().deleteAccount();
            return null;
        }, new Async.Callback<Object>() {
            @Override
            public void onSuccess(Object value) {
                app.sessions().signOut();
                toast("账户已注销");
                finish();
            }

            @Override
            public void onError(ApiException e) {
                toast("注销失败：" + e.getMessage());
            }
        });
    }

    // ---------- 对话框 ----------

    private EditText dialogField(String hint, String value, int inputType, int maxLength) {
        EditText e = new EditText(this);
        e.setHint(hint);
        e.setText(value);
        e.setSingleLine(true);
        e.setInputType(inputType);
        e.setFilters(new InputFilter[] {new InputFilter.LengthFilter(maxLength)});
        e.setMinHeight(dp(48));
        e.setSelection(e.getText().length());
        return e;
    }

    private LinearLayout dialogBox(View... children) {
        LinearLayout box = new LinearLayout(this);
        box.setOrientation(LinearLayout.VERTICAL);
        box.setPadding(dp(20), dp(8), dp(20), 0);
        for (View c : children) {
            box.addView(c);
        }
        return box;
    }

    private TextView dialogError(LinearLayout box) {
        TextView t = new TextView(this);
        t.setTextColor(getColor(R.color.danger));
        t.setTextSize(13);
        t.setVisibility(View.GONE);
        t.setAccessibilityLiveRegion(View.ACCESSIBILITY_LIVE_REGION_POLITE);
        box.addView(t);
        return t;
    }

    private void showDialogError(TextView error, EditText field, String message) {
        error.setText(message);
        error.setVisibility(View.VISIBLE);
        field.setError(message);
        field.requestFocus();
    }
}
