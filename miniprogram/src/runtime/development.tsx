import { useState } from 'react'
import { View, Text, Input, Button } from '@tarojs/components'
import Taro from '@tarojs/taro'
import { developmentLogin } from './platform'
import { application } from './session'

export function DevelopmentAccount() {
  const [account, setAccount] = useState(Taro.getStorageSync('cp_development_account') || 'tester-1')
  const [notice, setNotice] = useState('')
  if (!developmentLogin) return null
  async function select() {
    if (!/^[a-zA-Z0-9_-]{1,64}$/.test(account)) { setNotice('账号只允许字母、数字、下划线和横线'); return }
    try {
      if (application.globalData.refreshToken) await application.logout()
      Taro.setStorageSync('cp_development_account', account)
      await application.login()
      setNotice('已登录开发账号 ' + account)
      await Taro.reLaunch({ url: '/pages/profile/profile' })
    } catch (error: any) { setNotice(error.message || '开发登录失败') }
  }
  return <View className='card development-panel'>
    <Text className='heading'>本地测试账号</Text>
    <Text className='text-secondary'>不同账号的订单、钱包与卡片相互隔离。此环境使用模拟支付，不扣取真实资金。</Text>
    <Input aria-label='开发账号' value={account} onInput={e => setAccount(e.detail.value)} placeholder='例如 tester-1 / tester-2' />
    <Button size='mini' onClick={select}>登录 / 切换账号</Button>
    {notice && <Text>{notice}</Text>}
  </View>
}
