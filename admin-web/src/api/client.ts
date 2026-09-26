import axios, { AxiosError, AxiosInstance } from 'axios';

const BASE = import.meta.env.VITE_API_BASE || '';

export interface ApiEnvelope<T = unknown> {
  code: number;
  message: string;
  data: T | null;
  request_id: string;
}

export const http: AxiosInstance = axios.create({
  baseURL: BASE,
  timeout: 15_000,
});

http.interceptors.request.use((cfg) => {
  const t = localStorage.getItem('cp_token');
  if (t) cfg.headers.Authorization = `Bearer ${t}`;
  return cfg;
});

http.interceptors.response.use(
  (resp) => {
    const env = resp.data as ApiEnvelope;
    if (env && env.code !== 0) {
      const err = new Error(env.message || 'api error');
      (err as any).code = env.code;
      (err as any).request_id = env.request_id;
      throw err;
    }
    return resp;
  },
  (err: AxiosError) => {
    if (err.response?.status === 401) {
      localStorage.removeItem('cp_token');
      window.location.href = '/admin/login';
    }
    return Promise.reject(err);
  }
);

export async function apiGet<T>(path: string, params?: Record<string, unknown>): Promise<T> {
  const r = await http.get<ApiEnvelope<T>>(path, { params });
  return (r.data.data ?? null) as T;
}

export async function apiPost<T>(path: string, body?: unknown): Promise<T> {
  const r = await http.post<ApiEnvelope<T>>(path, body);
  return (r.data.data ?? null) as T;
}

export async function apiPut<T>(path: string, body?: unknown): Promise<T> {
  const r = await http.put<ApiEnvelope<T>>(path, body);
  return (r.data.data ?? null) as T;
}

export async function apiDelete<T>(path: string): Promise<T> {
  const r = await http.delete<ApiEnvelope<T>>(path);
  return (r.data.data ?? null) as T;
}