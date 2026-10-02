import type { FileUpload, StoredFile } from '../types/api';
import { request, requestBlob } from './http';

// 私有文件：上传后只有本人和管理员能读取。类型和大小由服务端按内容判断，这里不做预判，
// 错误（unsupported_file_type、payload_too_large、object_storage_unavailable）以 ApiError 抛出，message 可直接展示。

export async function uploadFile(file: Blob, filename = 'upload', signal?: AbortSignal): Promise<StoredFile> {
  const form = new FormData();
  form.append('file', file, filename);
  const res = await request<FileUpload>('/files', { method: 'POST', body: form, signal });
  return res.file;
}

const FILE_URL_PREFIX = '/api/v1/files/';

// fileIdFromURL 从 /api/v1/files/{id} 形式的地址取出 file_id；不是私有文件地址时返回 null。
export function fileIdFromURL(url: string): string | null {
  if (!url.startsWith(FILE_URL_PREFIX)) return null;
  const id = url.slice(FILE_URL_PREFIX.length);
  return /^file_[A-Za-z0-9_]{1,64}$/.test(id) ? id : null;
}

// fetchFileBlob 带登录 token 下载私有文件。<img src> 不能带 Authorization 头，token 也不能放进 URL，
// 所以展示私有图片要先取回字节，再用 URL.createObjectURL 生成本地地址，用完后 revokeObjectURL。
export function fetchFileBlob(fileId: string, signal?: AbortSignal): Promise<Blob> {
  return requestBlob(`/files/${encodeURIComponent(fileId)}`, { signal });
}
