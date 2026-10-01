import axios, { type AxiosError, type AxiosInstance } from 'axios';
import { apiErrorMessage } from './errorMessage';
import { createSessionManager, SessionExpired, type SessionTokens } from './session';
import type { InternalAxiosRequestConfig } from 'axios';

const BASE = import.meta.env?.VITE_API_BASE || '';

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

let sessionQueue: Promise<unknown> = Promise.resolve();
const sessionLock = async <T,>(action: () => Promise<T>): Promise<T> => {
  if (navigator.locks) return await navigator.locks.request('cp-admin-session', action);
  const next = sessionQueue.then(action, action);
  sessionQueue = next.catch(() => undefined);
  return next;
};
export const adminSession = createSessionManager(localStorage, sessionLock, () => window.dispatchEvent(new Event('cp-session')), () => crypto.randomUUID());
type SessionRequest = InternalAxiosRequestConfig & { cpEpoch?: string | null; cpSentToken?: string; cpRetried?: boolean };
const createsSession = (url?: string) => url === '/api/v1/admin/auth/login' || url === '/api/v1/admin/auth/mfa';
const isExpiredSession = (error: unknown) => error instanceof SessionExpired || (axios.isAxiosError(error) && error.response?.status === 401);
const requestSessionTokens = async (refresh: string): Promise<SessionTokens> => {
  const response = await axios.post<ApiEnvelope<SessionTokens>>(`${BASE}/api/v1/admin/auth/refresh`, { refresh_token: refresh }, { timeout: 15000 });
  if (!response.data.data || response.data.code !== 0) throw new SessionExpired('登录已过期，请重新登录');
  return response.data.data;
};
const expireSession = async (expected: string | null) => {
  if (await adminSession.clear(expected)) window.location.href = '/admin/login';
};

http.interceptors.request.use(async (cfg: SessionRequest) => {
  if (cfg.cpEpoch === undefined) cfg.cpEpoch = adminSession.epoch();
  if (cfg.cpEpoch !== adminSession.epoch()) throw new Error('登录账号已变化，请重新打开页面');
  if (!createsSession(cfg.url)) {
    try {
      await adminSession.ensureFresh(cfg.cpEpoch, requestSessionTokens);
    } catch (error) {
      if (isExpiredSession(error)) await expireSession(cfg.cpEpoch);
      if (axios.isAxiosError(error)) error.message = apiErrorMessage(error.response?.status, error.response?.data, error.code);
      throw error;
    }
  }
  if (cfg.cpEpoch !== adminSession.epoch()) throw new Error('登录账号已变化，请重新打开页面');
  const t = localStorage.getItem('cp_token');
  if (t) cfg.headers.Authorization = `Bearer ${t}`;
  cfg.cpSentToken = t || '';
  return cfg;
});

http.interceptors.response.use(
  (resp) => {
    const cfg = resp.config as SessionRequest;
    if (cfg.cpEpoch !== adminSession.epoch()) throw new Error('登录账号已变化，请重新打开页面');
    const env = resp.data as ApiEnvelope;
    if (env && env.code !== 0) {
      const err = new Error(env.message || 'api error');
      (err as any).code = env.code;
      (err as any).request_id = env.request_id;
      throw err;
    }
    return resp;
  },
  async (err: AxiosError) => {
    if (!axios.isAxiosError(err)) return Promise.reject(err);
    err.message = apiErrorMessage(err.response?.status, err.response?.data, err.code);
    const isLogin = err.config?.url === '/api/v1/admin/auth/login';
    if (isLogin && err.response?.status === 401) err.message = '用户名或密码错误';
    if (isLogin && err.response?.status === 404) err.message = '后台登录接口不可用，请确认后端服务已更新';
    const cfg = err.config as SessionRequest | undefined;
    // 预刷新使用独立 axios；其错误没有业务请求的 epoch，不能再次刷新或清理其他会话。
    if (err.response?.status === 401 && !createsSession(cfg?.url) && cfg?.cpEpoch !== undefined) {
      if (cfg && !cfg.cpRetried) {
        cfg.cpRetried = true;
        try {
          await adminSession.refresh(cfg.cpSentToken || '', cfg.cpEpoch, requestSessionTokens);
          return http.request(cfg);
        } catch (refreshError) {
          if (!isExpiredSession(refreshError)) {
            if (axios.isAxiosError(refreshError)) refreshError.message = apiErrorMessage(refreshError.response?.status, refreshError.response?.data, refreshError.code);
            return Promise.reject(refreshError);
          }
        }
      }
      await expireSession(cfg.cpEpoch);
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

export async function apiDelete<T>(path: string, body?: unknown): Promise<T> {
  const r = await http.delete<ApiEnvelope<T>>(path, { data: body });
  return (r.data.data ?? null) as T;
}
