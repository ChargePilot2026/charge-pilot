/// <reference types="miniprogram-api-typings" />

declare const wx: any;

interface WxApp {
  globalData: {
    apiBase: string;
    userInfo: import('../src/runtime/models').ChargeUserProfile | null;
    token: string;
    refreshToken: string;
  };
  logout(): Promise<void>;
  login(): Promise<import('../src/runtime/models').UserLogin>;
  request<T>(method: string, path: string, data?: unknown, auth?: boolean): Promise<T>;
}

declare const App: (options: any) => WxApp;
declare const Page: (options: any) => void;
declare const Component: (options: any) => void;
