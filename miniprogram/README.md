# 充电用户端：Taro + React

用户端使用 Taro 4.3、React 18 和 TypeScript，同一份源代码构建浏览器 H5 与微信小程序。界面组件使用 Taro UI 3.4。

推荐 Node.js 24，使用已提交的 lockfile 安装依赖：

```sh
npm ci
npm run dev:h5
npm run typecheck
npm test
npm run build:h5
npm run build:weapp
```

H5 开发服务器默认端口 5174。本机联调使用仓库根目录 `compose.dev.yaml`，已设置代理、开发登录与模拟支付，操作见 [本地运行](../README.md#本地运行)。

## UI 组件库

界面组件统一使用 `taro-ui`，样式按需引入，入口为 `src/taro-ui.scss`，由 `src/app.tsx` 在 `app.css` 之前引入，使本项目样式可覆盖组件库默认值。

底部导航是 `src/runtime/TabBar.tsx` 提供的自定义组件，全部用户端页面共用，样式在 `src/runtime/TabBar.css` 并由组件自身引入。共用组件的样式必须跟随组件，不能放进某个页面的样式文件——否则只有该页面能得到布局。导航条为 `fixed` 定位，组件在导航条之前输出 `.tab-bar-spacer` 占位块，统一为各页面预留底部空间，页面样式不需要再自行留白。

- **尺寸基准为 750，与 `designWidth: 750` 一致。** Taro UI 以 `$hd: 2` 编写字号（如 `14px * $hd` 即 28px），已按 750 基准放大，不需要覆盖任何尺寸变量。业务样式继续沿用 `rpx`，两者互不影响。
- **新增组件时在 `src/taro-ui.scss` 追加一行** `@import 'taro-ui/dist/style/components/<name>.scss';`。全量样式 `taro-ui/dist/style/index.css` 约 280 KB，在小程序主包 2 MB 限制下不采用。
- **React 版本上限为 18。** `@tarojs/react@4.3.0` 的 peer 为 `react: ^18`，Taro 未放开 React 19；Taro UI 要求 `react >=16.13.0`，二者相容。

`.npmrc` 的 `legacy-peer-deps` 不可移除：Taro UI 对 `@tarojs/taro-rn` 和多个 `@tarojs/plugin-platform-*` 声明了非可选 peer，默认解析会引入 React Native 并与 React 18 冲突。

| 环境变量 | 作用 |
| --- | --- |
| `TARO_APP_API_BASE` | API 地址，H5 开发默认为 `/api/v1`；微信端须指定可访问的绝对 HTTPS API 地址 |
| `DEV_API_PROXY_TARGET` | H5 开发代理目标，默认 `http://127.0.0.1:8080` |
| `TARO_APP_LOGIN_MODE=development` | 显式启用本地账号；须与后端 `LOGIN_MODE=development`、`PAYMENT_MODE=simulation` 配套 |

未开启 development 时使用正式微信登录。模拟支付需后端明确开启 simulation，仅允许当前用户确认自己的模拟预付单，客户端不包含内部服务令牌。H5 中暂不启用正式微信商户支付。

产物位于 `dist/h5` 和 `dist/weapp`，均不提交。微信开发工具导入 `dist/weapp`，自行填写申请后的 AppID；构建不需要 AppID。H5 定位失败可以填写经纬度，扫码页面可以手动输入设备或端口编号。
