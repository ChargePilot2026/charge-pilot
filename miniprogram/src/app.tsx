import { useLaunch, useDidShow } from '@tarojs/taro'
import type { PropsWithChildren } from 'react'
import { application } from './runtime/session'
import './taro-ui.scss'
import './app.css'

export default function App({ children }: PropsWithChildren) {
  useLaunch(() => application.onLaunch())
  useDidShow(options => application.onShow(options))
  return children
}
