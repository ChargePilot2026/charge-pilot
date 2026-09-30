# 充电用户端：Taro + React

21 个业务页面共用 Taro 4.3、React 18 和 TypeScript；同一份源代码构建浏览器 H5 与微信小程序。路由与平台 API 由 Taro 管理，原业务控制器通过 React 生命周期适配。

推荐 Node.js 24，使用已提交的 lockfile 安装依赖：

```sh
npm ci
npm run dev:h5
npm run typecheck
npm test
npm run build:h5
npm run build:weapp
```

H5 开发服务器默认端口 5174。本机联调使用仓库根目录 `compose.dev.yaml`，已设置代理、本地登录与模拟支付，操作见 [本地验收说明](../docs/local-test-guide.md)。

| 环境变量 | 作用 |
| --- | --- |
| `TARO_APP_API_BASE` | API 地址，H5 开发默认为 `/api/v1`；微信端须指定可访问的绝对 HTTPS API 地址 |
| `DEV_API_PROXY_TARGET` | H5 开发代理目标，默认 `http://127.0.0.1:8080` |
| `TARO_APP_LOGIN_MODE=development` | 显式启用本地账号；须与后端 `LOGIN_MODE=development`、`PAYMENT_MODE=simulation` 配套 |

未开启 development 时使用正式微信登录。模拟支付需后端明确开启 simulation，仅允许当前用户确认自己的模拟预付单，客户端不包含内部服务令牌。H5 中暂不启用正式微信商户支付。

产物位于 `dist/h5` 和 `dist/weapp`，均不提交。微信开发工具导入 `dist/weapp`，自行填写申请后的 AppID；构建不需要 AppID。H5 定位失败可以填写经纬度，扫码页面可以手动输入设备或端口编号。
