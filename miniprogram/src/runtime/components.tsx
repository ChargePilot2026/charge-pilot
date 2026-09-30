import Taro from '@tarojs/taro'
import { View, Text, Map, Button } from '@tarojs/components'
import { isH5 } from './platform'
import type { CSSProperties } from 'react'

export function inlineStyle(value: Record<string, string>): CSSProperties {
  return Object.fromEntries(Object.entries(value).map(([key, text]) => [key, text.replace(/([\d.]+)rpx/g, (_, n) => Taro.pxTransform(Number(n))) ]))
}

export function StationMap(props: any) {
  if (!isH5) return <Map {...props} />
  return <View className='map-summary'>
    <Text>当前位置：{Number(props.latitude).toFixed(5)}, {Number(props.longitude).toFixed(5)}</Text>
    <Text className='text-secondary'>H5 地图通过站点列表查看；点击站点可打开浏览器地图导航。</Text>
    {(props.markers || []).map((marker: any) => <Button size='mini' key={marker.id}
      onClick={() => props.onMarkerTap?.({ detail: { markerId: marker.id }, currentTarget: { dataset: {} } })}>{marker.title}</Button>)}
  </View>
}
