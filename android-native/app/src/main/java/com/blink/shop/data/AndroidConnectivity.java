package com.blink.shop.data;

import android.content.Context;
import android.net.ConnectivityManager;
import android.net.Network;
import android.net.NetworkCapabilities;

import com.blink.shop.net.ApiClient;

/** 用系统网络状态判断是否联网；拿不到状态时按联网处理，交给请求本身失败。 */
public final class AndroidConnectivity implements ApiClient.Connectivity {

    private final ConnectivityManager cm;

    public AndroidConnectivity(Context context) {
        cm = (ConnectivityManager) context.getSystemService(Context.CONNECTIVITY_SERVICE);
    }

    @Override
    public boolean isOnline() {
        if (cm == null) {
            return true;
        }
        try {
            Network n = cm.getActiveNetwork();
            if (n == null) {
                return false;
            }
            NetworkCapabilities caps = cm.getNetworkCapabilities(n);
            return caps != null && caps.hasCapability(NetworkCapabilities.NET_CAPABILITY_INTERNET);
        } catch (SecurityException e) {
            return true;
        }
    }
}
