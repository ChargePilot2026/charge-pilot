import { Fragment } from "react";
import { Text, View } from "@tarojs/components";
import { AtIcon } from "taro-ui";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import controller from "./index.controller";
import "./index.css";

// 设计稿的四个功能入口。url 为空表示该入口尚未上线，控制器会明确提示而不跳转。
const ENTRIES = [
  { key: "nearby", title: "附近电站", sub: "查看附近电站", icon: "map-pin", action: "goMap" },
  { key: "frequent", title: "常用电站", sub: "最近使用的电站", icon: "lightning-bolt", action: "goFrequent" },
  { key: "orders", title: "订单记录", sub: "查看充电订单", icon: "file-generic", action: "goOrders" },
  { key: "deviceMap", title: "设备地图", sub: "发现附近充电设备", icon: "equalizer", action: "goDeviceMap" },
];

export default function PageView() {
const { data, event } = useController(controller);
const stations = data.stations || [];
return (<><View className="home">
  <View className="home-entries">{ENTRIES.map((entry) => (<View className="entry-card" onClick={event(entry.action, {})} key={entry.key}>
    <View className="entry-body"><Text className="entry-title">{entry.title}</Text>
    <Text className="entry-sub">{entry.sub}</Text></View>
    <AtIcon value={entry.icon} className="entry-icon" /></View>))}</View>

  <View className="section-head">
    <View className="section-title"><View className="section-bar" />
    <Text>{"附近电站"}</Text></View>
    <View className="section-action" onClick={event("relocate", {})}><AtIcon value="reload" className="section-action-icon" />
    <Text>{"重新定位"}</Text></View>
  </View>

  {!!(data.locating) && (<View className="station-hint">{"正在获取位置…"}</View>)}
  {!!(data.locationNotice) && (<View className="station-hint">{data.locationNotice}
  <Text className="station-hint-link" onClick={event("relocate", {})}>{"重新定位"}</Text></View>)}
  {!!(data.mapError) && (<View className="station-hint station-hint-error">{data.mapError}
  <Text className="station-hint-link" onClick={event("relocate", {})}>{"重试"}</Text></View>)}

  {stations.map((station: any, index: number) => (<View className="station-card" data-id={station?.id} onClick={event("goStation", {})} key={String(station?.id ?? index)}>
    <View className="station-head">
      <Text className="station-name">{station?.name || '未命名电站'}</Text>
      <Text className="station-distance">{station?.distanceText || '距离未知'}</Text>
    </View>
    <Text className="station-address">{station?.address || '地址未填写'}</Text>
  </View>))}

  {!!(!stations.length && !data.mapError && !data.locating) && (<View className="station-hint">{"暂无可显示的附近电站，可点击右上角重新定位。"}</View>)}
</View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
