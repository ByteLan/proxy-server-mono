export class APIError extends Error {
  constructor(message: string, public status: number) {
    super(message);
  }
}

export async function request<T>(token: string, path: string, options: RequestInit = {}): Promise<T> {
  const headers = new Headers(options.headers);
  headers.set('Authorization', `Bearer ${token}`);
  if (options.body) headers.set('Content-Type', 'application/json');
  const response = await fetch(`/api/admin${path}`, { ...options, headers, cache: 'no-store' });
  if (!response.ok) {
    let detail = `请求失败（${response.status}）`;
    try {
      const payload = await response.json();
      if (typeof payload.error === 'string') detail = payload.error;
    } catch { /* Response body may be empty after a reverse proxy failure. */ }
    throw new APIError(response.status === 401 ? '管理令牌无效或已失效，请重新登录。' : detail, response.status);
  }
  if (response.status === 204) return undefined as T;
  if (response.headers.get('content-type')?.includes('json')) return response.json() as Promise<T>;
  return response.text() as Promise<T>;
}
