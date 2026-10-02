import { expect, test } from '@playwright/test';

// 私有文件：真实 API + MinIO。用 Web 的上传 helper（frontend/src/api/files.ts）在浏览器里上传和下载，
// 验证 token 只放在请求头、类型按内容判断、他人无法读取。

const USER = { username: 'blink_user', password: 'BlinkDev#2026' };
const OTHER = { username: 'blink_user2', password: 'BlinkDev#2026' };
// 1×1 PNG。
const PNG_BASE64 = 'iVBORw0KGgoAAAANSUhEUgAAAAEAAAABCAYAAAAfFcSJAAAADUlEQVR4nGP438DwHwAGgAJ/EEwb4QAAAABJRU5ErkJggg==';

async function apiLogin(request, user) {
  const res = await request.post('/api/v1/auth/login', { data: user });
  expect(res.ok(), `login ${user.username}`).toBeTruthy();
  return res.json();
}

async function signedIn(page, session) {
  await page.addInitScript((s) => window.localStorage.setItem('blink_shop.session', JSON.stringify(s)), session);
  await page.goto('/');
}

test('上传图片后本人可下载，他人 403，未登录 401', async ({ page, request }) => {
  const session = await apiLogin(request, USER);
  await signedIn(page, session);

  const requests = [];
  page.on('request', (r) => {
    if (r.url().includes('/api/v1/files')) requests.push({ url: r.url(), auth: r.headers().authorization });
  });

  const result = await page.evaluate(async (b64) => {
    const { uploadFile, fetchFileBlob, fileIdFromURL } = await import('/src/api/files.ts');
    const bytes = Uint8Array.from(atob(b64), (c) => c.charCodeAt(0));
    // 声明类型和文件名都是假的，服务端只看内容。
    const file = await uploadFile(new Blob([bytes], { type: 'text/html' }), '../../evil.php');
    const blob = await fetchFileBlob(fileIdFromURL(file.url));
    const back = new Uint8Array(await blob.arrayBuffer());
    return { file, type: blob.type, same: back.length === bytes.length && back.every((v, i) => v === bytes[i]) };
  }, PNG_BASE64);

  expect(result.file.file_id).toMatch(/^file_[0-9a-f]{24}$/);
  expect(result.file.url).toBe(`/api/v1/files/${result.file.file_id}`);
  expect(result.file.mime_type).toBe('image/png');
  expect(result.file.size_bytes).toBe(Buffer.from(PNG_BASE64, 'base64').length);
  expect(result.file).not.toHaveProperty('object_key');
  expect(result.type).toBe('image/png');
  expect(result.same).toBe(true);

  // 两次请求都带 Bearer 头，URL 中没有 token。
  expect(requests).toHaveLength(2);
  for (const r of requests) {
    expect(r.auth).toBe(`Bearer ${session.token}`);
    expect(r.url).not.toContain(session.token);
  }

  // 响应头：私有缓存、禁止嗅探、沙箱。
  const own = await request.get(result.file.url, { headers: { Authorization: `Bearer ${session.token}` } });
  expect(own.status()).toBe(200);
  expect(own.headers()['cache-control']).toBe('private, max-age=3600');
  expect(own.headers()['x-content-type-options']).toBe('nosniff');
  expect(own.headers()['content-security-policy']).toBe("default-src 'none'; sandbox");

  const other = await apiLogin(request, OTHER);
  const denied = await request.get(result.file.url, { headers: { Authorization: `Bearer ${other.token}` } });
  expect(denied.status()).toBe(403);
  expect((await denied.json()).code).toBe('forbidden');
  const anonymous = await request.get(result.file.url);
  expect(anonymous.status()).toBe(401);
});

test('不支持的类型和不存在的文件返回可展示的错误', async ({ page, request }) => {
  await signedIn(page, await apiLogin(request, USER));
  const errors = await page.evaluate(async () => {
    const { uploadFile, fetchFileBlob } = await import('/src/api/files.ts');
    const capture = (p) => p.then(() => null, (e) => ({ name: e.name, status: e.status, code: e.code, field: e.field, message: e.message }));
    return {
      text: await capture(uploadFile(new Blob(['just text'], { type: 'image/png' }), 'fake.png')),
      svg: await capture(uploadFile(new Blob(['<svg xmlns="http://www.w3.org/2000/svg"/>'], { type: 'image/svg+xml' }), 'a.svg')),
      empty: await capture(uploadFile(new Blob([]), 'empty.png')),
      missing: await capture(fetchFileBlob('file_0123456789abcdef01234567')),
    };
  });
  expect(errors.text).toEqual({ name: 'ApiError', status: 400, code: 'unsupported_file_type', field: 'file', message: '不支持该文件类型，仅支持 JPG、PNG、WEBP、GIF、PDF' });
  expect(errors.svg).toMatchObject({ status: 400, code: 'unsupported_file_type' });
  expect(errors.empty).toMatchObject({ status: 400, code: 'invalid_argument', field: 'file', message: '请选择要上传的文件' });
  expect(errors.missing).toMatchObject({ status: 404, code: 'file_not_found', message: '文件不存在' });
});

test('登录失效时下载返回 401 并清除本地会话', async ({ page, request }) => {
  const session = await apiLogin(request, USER);
  const upload = await request.post('/api/v1/files', {
    headers: { Authorization: `Bearer ${session.token}` },
    multipart: { file: { name: 'a.png', mimeType: 'image/png', buffer: Buffer.from(PNG_BASE64, 'base64') } },
  });
  expect(upload.status()).toBe(201);
  const { file } = await upload.json();

  await signedIn(page, { ...session, token: 'expired-or-revoked-token' });
  const result = await page.evaluate(async (id) => {
    const { fetchFileBlob } = await import('/src/api/files.ts');
    const status = await fetchFileBlob(id).then(() => 200, (e) => e.status);
    return { status, session: window.localStorage.getItem('blink_shop.session') };
  }, file.file_id);
  expect(result).toEqual({ status: 401, session: null });
});
