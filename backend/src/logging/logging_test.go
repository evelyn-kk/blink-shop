package logging

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
)

func TestMaskText(t *testing.T) {
	tests := []struct {
		name string
		in   string
		want string
	}{
		{"phone", "联系 13812345678 发货", "联系 138****5678 发货"},
		{"phone with +86", "+8613812345678", "+86138****5678"},
		{"two phones", "13812345678,13987654321", "138****5678,139****4321"},
		{"not a phone: too long", "order 213812345678901", "order 213812345678901"},
		{"not a phone: bad prefix", "12812345678", "12812345678"},
		{"email", "mail alice.w@example.com now", "mail a***@example.com now"},
		{"bearer", "Authorization: Bearer abc.def-123", "Authorization: Bearer ***"},
		{"plain", "nothing sensitive", "nothing sensitive"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := MaskText(tt.in); got != tt.want {
				t.Fatalf("MaskText(%q) = %q, want %q", tt.in, got, tt.want)
			}
		})
	}
}

func TestIsSensitiveKey(t *testing.T) {
	tests := map[string]bool{
		"Authorization": true,
		"password":      true,
		"new_password":  true,
		"token":         true,
		"access_token":  true,
		"api_key":       true,
		"API-Key":       true,
		"apiKey":        true,
		"client_secret": true,
		"Cookie":        true,
		"mysql_dsn":     true,
		"path":          false,
		"status":        false,
		"phone":         false, // 手机号按值掩码，保留部分可排障信息
	}
	for key, want := range tests {
		if got := IsSensitiveKey(key); got != want {
			t.Errorf("IsSensitiveKey(%q) = %v, want %v", key, got, want)
		}
	}
}

func TestLoggerRedacts(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo).With("api_key", "sk-live-123")

	logger.Info("user 13812345678 login",
		"Authorization", "Bearer secret-token",
		"password", "hunter2",
		"phone", "13812345678",
		"email", "bob@example.com",
		"err", errors.New("send to bob@example.com failed"),
		slog.Group("req", "token", "t-1", "path", "/api/v1/auth/login"),
	)

	out := buf.String()
	for _, leaked := range []string{"sk-live-123", "secret-token", "hunter2", "13812345678", "bob@example.com", "t-1"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log leaked %q: %s", leaked, out)
		}
	}

	var entry map[string]any
	if err := json.Unmarshal(buf.Bytes(), &entry); err != nil {
		t.Fatalf("log is not JSON: %v\n%s", err, out)
	}
	checks := map[string]string{
		"msg":           "user 138****5678 login",
		"api_key":       Masked,
		"Authorization": Masked,
		"password":      Masked,
		"phone":         "138****5678",
		"email":         "b***@example.com",
		"err":           "send to b***@example.com failed",
	}
	for k, want := range checks {
		if entry[k] != want {
			t.Errorf("%s = %v, want %q", k, entry[k], want)
		}
	}
	req, _ := entry["req"].(map[string]any)
	if req["token"] != Masked || req["path"] != "/api/v1/auth/login" {
		t.Errorf("group not redacted correctly: %v", req)
	}
}

type loginRequest struct {
	Username string            `json:"username"`
	Password string            `json:"password"`
	Phone    string            `json:"phone"`
	Extra    map[string]string `json:"extra"`
}

func TestLoggerRedactsStructsAndMaps(t *testing.T) {
	var buf bytes.Buffer
	logger := New(&buf, slog.LevelInfo)
	logger.Info("login",
		"req", loginRequest{Username: "alice", Password: "hunter2", Phone: "13812345678", Extra: map[string]string{"api_key": "k-1", "note": "mail a@b.com"}},
		"headers", map[string][]string{"Authorization": {"Bearer abc"}, "Accept": {"application/json"}},
		"list", []string{"13987654321"},
	)
	out := buf.String()
	for _, leaked := range []string{"hunter2", "13812345678", "k-1", "a@b.com", "Bearer abc", "13987654321"} {
		if strings.Contains(out, leaked) {
			t.Errorf("log leaked %q: %s", leaked, out)
		}
	}
	for _, kept := range []string{`"username":"alice"`, `"phone":"138****5678"`, `"Accept":["application/json"]`, `"note":"mail a***@b.com"`} {
		if !strings.Contains(out, kept) {
			t.Errorf("log should contain %s: %s", kept, out)
		}
	}
}
