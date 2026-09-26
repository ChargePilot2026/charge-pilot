/// <reference types="miniprogram-api-typings" />

declare const wx: any;

interface WxApp {
  globalData: {
    apiBase: string;
    userInfo: any;
    token: string;
  };
  login(): Promise<{ token: string; user_id: number; openid: string }>;
  request<T>(method: string, path: string, data?: unknown, auth?: boolean): Promise<T>;
}

declare const App: (options: any) => WxApp;
declare const Page: (options: any) => void;
declare const Component: (options: any) => void;