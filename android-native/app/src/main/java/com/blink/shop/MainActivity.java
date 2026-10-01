package com.blink.shop;

import android.app.Activity;
import android.os.Bundle;
import android.widget.TextView;

public class MainActivity extends Activity {

    @Override
    protected void onCreate(Bundle savedInstanceState) {
        super.onCreate(savedInstanceState);
        setContentView(R.layout.activity_main);

        ApiConfig apiConfig = new ApiConfig(BuildConfig.DEFAULT_API_BASE);
        TextView apiBase = findViewById(R.id.api_base);
        apiBase.setText(getString(R.string.api_base_label, apiConfig.baseUrl()));
    }
}
