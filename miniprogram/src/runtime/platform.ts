import Taro from '@tarojs/taro'

export const developmentLogin = process.env.TARO_APP_LOGIN_MODE === 'development'
export const isH5 = process.env.TARO_ENV === 'h5'

// Explicit Taro references let its compiler include the H5 platform APIs.
// Resolve them at call time rather than copying the pre-initialization object.
export const wx: any = new Proxy({
  request: (options: any) => Taro.request(options),
  getStorageSync: (key: string) => Taro.getStorageSync(key),
  setStorageSync: (key: string, value: any) => Taro.setStorageSync(key, value),
  removeStorageSync: (key: string) => Taro.removeStorageSync(key),
  navigateTo: (options: any) => Taro.navigateTo(options),
  navigateBack: (options: any) => Taro.navigateBack(options),
  switchTab: (options: any) => Taro.switchTab(options),
  showModal: (options: any) => Taro.showModal(options),
  showToast: (options: any) => Taro.showToast(options),
  stopPullDownRefresh: (options?: any) => Taro.stopPullDownRefresh(options),
  getLocation: (options: any) => Taro.getLocation(options),
  openLocation: (options: any) => Taro.openLocation(options),
  makePhoneCall: (options: any) => Taro.makePhoneCall(options),
  setClipboardData: (options: any) => Taro.setClipboardData(options),
  login(options: any) {
    if (!developmentLogin) return Taro.login(options)
    const account = Taro.getStorageSync('cp_development_account') || 'tester-1'
    const result = { code: 'dev:' + account, errMsg: 'login:ok' }
    options.success?.(result); options.complete?.(result)
    return Promise.resolve(result)
  },
  async requestPayment(options: any) {
    if (options.provider !== 'simulation') {
      if (isH5) { const error = { errMsg: 'H5 当前仅支持本地模拟支付；微信支付需在微信端配置商户' }; options.fail?.(error); return }
      return Taro.requestPayment(options)
    }
    try {
      const accepted = await Taro.showModal({ title: '开发模拟支付', content: '确认后由服务端处理真实业务回调，不会扣取真实资金。' })
      if (!accepted.confirm) throw { errMsg: 'requestPayment:fail cancel' }
      // Token comes from the user's session. No internal service token is
      // exposed to browser code or frontend environment variables.
      const result = await Taro.request({ url: (process.env.TARO_APP_API_BASE || '/api/v1') + '/user/development/payments/confirm', method: 'POST',
        data: { merchant_order_no: options.merchantOrderNo }, header: { Authorization: 'Bearer ' + Taro.getStorageSync('cp_token') } })
      if (result.statusCode !== 200 || result.data?.code !== 0) throw new Error(result.data?.message || '模拟支付未确认')
      options.success?.({ errMsg: 'requestPayment:ok' }); options.complete?.({})
    } catch (error: any) { options.fail?.({ errMsg: error.errMsg || error.message }); options.complete?.(error) }
  },
  scanCode(options: any) {
    if (!isH5) return Taro.scanCode(options)
    const error = { errMsg: '当前浏览器请使用下方二维码内容输入框，可输入设备编号或端口码。' }
    options.fail?.(error); options.complete?.(error)
  },
  openCustomerServiceChat(options: any) {
    if (!isH5) return Taro.openCustomerServiceChat(options)
    const url = options.extInfo?.url
    if (typeof url === 'string' && /^https:\/\//.test(url)) { window.open(url, '_blank', 'noopener,noreferrer'); options.success?.({}); options.complete?.({}) }
    else { options.fail?.({ errMsg: '客服入口未配置有效 HTTPS 地址' }); options.complete?.({}) }
  },
}, { get(target: any, key: string) { return key in target ? target[key] : (Taro as any)[key] } })
