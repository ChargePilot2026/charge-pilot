# ChargePilot · 微信小程序

## 目录结构

```
miniprogram/
├── app.json                  小程序配置
├── app.ts                    App 入口 + request 封装
├── app.wxss                  全局样式
├── sitemap.json              搜索索引
├── types/global.d.ts         TypeScript 类型声明
├── pages/                    页面(每个页面 .ts/.wxml/.wxss/.json 四件套)
│   ├── index/                首页(扫码入口)
│   ├── scan/                 扫码中
│   ├── scan-result/          扫码结果(端口 / 设备)
│   ├── charge/
│   │   ├── charging.ts       充电中(5s 轮询)
│   │   ├── history.ts        历史订单
│   │   └── detail.ts         订单详情
│   ├── stations/             找桩(地图)
│   ├── wallet/               钱包(余额 / 充值 / 流水 / 退款)
│   ├── coupons/              我的优惠券
│   ├── invoice/              发票
│   ├── profile/              个人中心 + 手机号绑定
│   ├── announcement/         公告
│   ├── cs/                   客服
│   └── dev/                  报修
└── components/               自定义组件
```

## 配置

部署前修改 `app.ts`:
```ts
globalData: {
  apiBase: 'https://your-domain.com/api/v1',
}
```

## 开发

1. 微信开发者工具导入此目录,AppID 填客户小程序 AppID
2. 后端服务启动后,把 `apiBase` 改为本地调试地址(如 `http://localhost:8081/api/v1`,但需在开发者工具勾选"不校验合法域名")
3. 真机调试:需要走 ngrok / frp 把后端暴露成 https,或者在客户域名上加调试白名单

## 发布

1. `miniprogram-ci` 自动上传:见 `.github/workflows/`
2. 微信小程序后台 → 开发管理 → 配置小程序服务器域名为客户域名
3. 提交审核 → 发布

## 关键 API(技术规格 § 3.2.2)

| 路径 | 用途 |
| --- | --- |
| `POST /api/v1/public/auth/login` | code → JWT |
| `POST /api/v1/user/scan/resolve` | 扫码解析 |
| `POST /api/v1/user/scan/port` | 单端口详情 |
| `POST /api/v1/user/scan/start` | 创建订单 + 微信预下单 |
| `GET /api/v1/user/charge/ongoing/snapshot` | 5s 轮询 |
| `POST /api/v1/user/charge/stop` | 主动停止 |
| `GET /api/v1/user/wallet/balance` | 余额 |
| `GET /api/v1/user/coupon/my` | 我的优惠券 |
| `GET /api/v1/user/announcement/list` | 公告 |
| `POST /api/v1/public/payment/wechat/callback` | 微信支付回调 |