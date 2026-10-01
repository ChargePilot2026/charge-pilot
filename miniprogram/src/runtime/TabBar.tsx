import { Fragment } from 'react'
import { View, Text } from '@tarojs/components'
import { AtIcon } from 'taro-ui'
import { wx } from './platform'
import './TabBar.css'

export interface TabItem {
  key: string
  label: string
  icon: string
  /** 目标页面路径。 */
  url: string
}

export const HOME_TABS: TabItem[] = [
  { key: 'home', label: '首页', icon: 'home', url: '/pages/index/index' },
  { key: 'scan', label: '扫码', icon: 'camera', url: '/pages/scan/scan' },
  { key: 'profile', label: '个人中心', icon: 'user', url: '/pages/profile/profile' },
]

// selectTab 自行完成跳转，页面控制器无需再实现一遍导航回调。
function selectTab(item: TabItem) {
  if (item.url) { wx.reLaunch({ url: item.url }); return }
  wx.showToast({ title: item.label + '即将上线', icon: 'none' })
}

// 底部导航采用自定义实现：小程序原生 tabBar 无法表达设计稿中居中悬浮的扫码按钮。
// active 决定高亮项：除个人中心外各页面统一高亮首页。
// 导航条 fixed 定位，前置占位块负责在各页面内容末尾预留底部空间。
export function TabBar(props: { active: string; tabs?: TabItem[] }) {
  const tabs = props.tabs ?? HOME_TABS
  return (
    <Fragment>
      <View className='tab-bar-spacer' />
      <View className='tab-bar'>
        {tabs.map((item) => {
          const active = item.key === props.active
          const isScan = item.key === 'scan'
          return (
            <View
              className={`tab-item${isScan ? ' tab-item-scan' : ''}${active ? ' tab-item-active' : ''}`}
              onClick={() => selectTab(item)}
              key={item.key}
            >
              <View className='tab-icon-wrap'>
                <AtIcon value={item.icon} className='tab-icon' />
              </View>
              <Text className='tab-label'>{item.label}</Text>
            </View>
          )
        })}
      </View>
    </Fragment>
  )
}
