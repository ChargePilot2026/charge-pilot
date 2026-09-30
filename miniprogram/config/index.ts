import { defineConfig } from '@tarojs/cli'

export default defineConfig({
  projectName: 'chargepilot-user',
  date: '2026-10-01',
  designWidth: 750,
  deviceRatio: { 640: 2.34 / 2, 750: 1, 828: 1.81 / 2 },
  sourceRoot: 'src',
  outputRoot: `dist/${process.env.TARO_ENV || 'h5'}`,
  framework: 'react',
  compiler: { type: 'vite', prebundle: { enable: false }, vitePlugins: [{
    name: 'chargepilot-home',
    configureServer(server) {
      server.middlewares.use((request, _response, next) => {
        if (request.url === '/') request.url = '/index.html'
        next()
      })
    },
  }] },
  env: {
    TARO_APP_API_BASE: JSON.stringify(process.env.TARO_APP_API_BASE || '/api/v1'),
    TARO_APP_LOGIN_MODE: JSON.stringify(process.env.TARO_APP_LOGIN_MODE || 'wechat'),
  },
  plugins: ['@tarojs/plugin-platform-h5', '@tarojs/plugin-platform-weapp'],
  mini: { postcss: { pxtransform: { enable: true }, cssModules: { enable: false } } },
  h5: {
    publicPath: '/',
    staticDirectory: 'static',
    router: { mode: 'hash' },
    devServer: {
      host: '0.0.0.0',
      port: 5174,
      open: false,
      watch: { usePolling: true, interval: 500 },
      proxy: { '/api': { target: process.env.DEV_API_PROXY_TARGET || 'http://127.0.0.1:8080', changeOrigin: true } },
    },
    postcss: { autoprefixer: { enable: true }, cssModules: { enable: false } },
  },
})
