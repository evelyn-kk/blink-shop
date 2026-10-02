package main

import (
	"bytes"
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"image"
	"image/png"
	"io"
	"mime/multipart"
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/evelyn-kk/blink-shop/backend/src/objectstore/miniotest"
	"github.com/evelyn-kk/blink-shop/backend/src/store/mysqlstore/mysqltest"
)

// startAPI 用给定环境变量启动完整进程并等待就绪；用例结束时优雅退出。
func startAPI(t *testing.T, env map[string]string) (base string, logs *syncBuffer) {
	t.Helper()
	addr := freeAddr(t)
	env["API_ADDR"] = addr
	env["RUN_MIGRATIONS"] = "true"
	ctx, cancel := context.WithCancel(context.Background())
	logs = &syncBuffer{}
	done := make(chan error, 1)
	go func() { done <- run(ctx, envOf(env), logs) }()
	t.Cleanup(func() {
		cancel()
		<-done
	})
	base = "http://" + addr + "/api/v1"
	waitUntilUp(t, base+"/health")
	deadline := time.Now().Add(15 * time.Second)
	for get(t, base+"/ready") != http.StatusOK {
		if time.Now().After(deadline) {
			t.Fatalf("API not ready; logs: %s", logs)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return base, logs
}

func registerUser(t *testing.T, base, username string) string {
	t.Helper()
	body := fmt.Sprintf(`{"username":%q,"password":"Passw0rd!x","display_name":"文件测试"}`, username)
	resp, err := http.Post(base+"/auth/register", "application/json", strings.NewReader(body))
	if err != nil {
		t.Fatal(err)
	}
	defer resp.Body.Close()
	var out struct {
		Token string `json:"token"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil || resp.StatusCode != http.StatusCreated || out.Token == "" {
		t.Fatalf("register %s: %d %v", username, resp.StatusCode, err)
	}
	return out.Token
}

func uploadPNG(t *testing.T, base, token string) (*http.Response, []byte) {
	t.Helper()
	var img bytes.Buffer
	_ = png.Encode(&img, image.NewRGBA(image.Rect(0, 0, 3, 3)))
	var body bytes.Buffer
	mw := multipart.NewWriter(&body)
	fw, _ := mw.CreateFormFile("file", "../../a.php")
	_, _ = fw.Write(img.Bytes())
	_ = mw.Close()
	req, _ := http.NewRequest(http.MethodPost, base+"/files", &body)
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { resp.Body.Close() })
	return resp, img.Bytes()
}

func countFiles(t *testing.T, dsn string) int {
	t.Helper()
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer db.Close()
	var n int
	if err := db.QueryRow(`SELECT COUNT(*) FROM stored_files`).Scan(&n); err != nil {
		t.Fatal(err)
	}
	return n
}

// 真实 MySQL + 真实 MinIO：上传写入对象和元数据，本人可下载、他人 403。
func TestFilesWithMinIO(t *testing.T) {
	dsn := mysqltest.FreshDSN(t)
	conn := miniotest.Fresh(t)
	base, logs := startAPI(t, map[string]string{
		"MYSQL_DSN": dsn, "MINIO_ENDPOINT": conn.Endpoint, "MINIO_ACCESS_KEY": conn.AccessKey,
		"MINIO_SECRET_KEY": conn.SecretKey, "MINIO_BUCKET": conn.Bucket,
	})
	owner := registerUser(t, base, "file_owner")
	resp, img := uploadPNG(t, base, owner)
	var up struct {
		File struct {
			FileID string `json:"file_id"`
			URL    string `json:"url"`
		} `json:"file"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&up); err != nil || resp.StatusCode != http.StatusCreated {
		t.Fatalf("upload: %d %v; logs: %s", resp.StatusCode, err, logs)
	}

	db, _ := sql.Open("mysql", dsn)
	defer db.Close()
	var key, mimeType string
	var size int64
	if err := db.QueryRow(`SELECT object_key, mime_type, size_bytes FROM stored_files WHERE file_id = ?`, up.File.FileID).Scan(&key, &mimeType, &size); err != nil {
		t.Fatal(err)
	}
	if !regexp.MustCompile(`^uploads/\d{4}/\d{2}/[0-9a-f]{32}\.png$`).MatchString(key) || mimeType != "image/png" || size != int64(len(img)) {
		t.Fatalf("row: key=%q mime=%q size=%d", key, mimeType, size)
	}

	download := func(token string) (int, []byte) {
		req, _ := http.NewRequest(http.MethodGet, strings.TrimSuffix(base, "/api/v1")+up.File.URL, nil)
		req.Header.Set("Authorization", "Bearer "+token)
		resp, err := http.DefaultClient.Do(req)
		if err != nil {
			t.Fatal(err)
		}
		defer resp.Body.Close()
		data, _ := io.ReadAll(resp.Body)
		return resp.StatusCode, data
	}
	if code, data := download(owner); code != http.StatusOK || !bytes.Equal(data, img) {
		t.Fatalf("owner download: %d, %d bytes", code, len(data))
	}
	if code, _ := download(registerUser(t, base, "file_other")); code != http.StatusForbidden {
		t.Fatalf("other user download: %d", code)
	}
}

// 对象存储不可达或未配置：上传返回 503 object_storage_unavailable，数据库没有残留记录。
func TestFilesObjectStorageDown(t *testing.T) {
	dsn := mysqltest.FreshDSN(t)
	for name, endpoint := range map[string]string{"unreachable": freeAddr(t), "not configured": ""} {
		t.Run(name, func(t *testing.T) {
			base, _ := startAPI(t, map[string]string{"MYSQL_DSN": dsn, "MINIO_ENDPOINT": endpoint})
			token := registerUser(t, base, "down_"+strings.ReplaceAll(name, " ", "_"))
			resp, _ := uploadPNG(t, base, token)
			var e struct {
				Code string `json:"code"`
			}
			_ = json.NewDecoder(resp.Body).Decode(&e)
			if resp.StatusCode != http.StatusServiceUnavailable || e.Code != "object_storage_unavailable" {
				t.Fatalf("upload: %d %q", resp.StatusCode, e.Code)
			}
			if n := countFiles(t, dsn); n != 0 {
				t.Fatalf("stored_files has %d rows", n)
			}
		})
	}
}
