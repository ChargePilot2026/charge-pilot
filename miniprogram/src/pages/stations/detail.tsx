import { Fragment } from "react";
import { View, Text } from "@tarojs/components";
import { AtIcon } from "taro-ui";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import controller from "./detail.controller";
import "./detail.css";

export default function PageView() {
const { data, event } = useController(controller);
const { station, announcements, devices, loading, error } = data;
return (<><View className="station-detail">
  {!!(loading) && (<View className="detail-hint">{"正在加载…"}</View>)}
  {!!(error) && (<View className="detail-hint detail-hint-error">{error}</View>)}

  {!!(station) && (<>
    <View className="detail-head">
      {announcements?.length > 0 && (<View className="notice-card">{announcements.slice(0,1).map((notice:any, i:number) => (<View key={String(notice?.id ?? i)}>
        <View className="notice-title"><View className="notice-dot" />
        <Text>{notice?.title || '站内公告'}</Text></View>
        {!!(notice?.content) && (<Text className="notice-content">{notice.content}</Text>)}
      </View>))}</View>)}
      <View className="detail-name">{station?.name || '未命名电站'}</View>
      <View className="detail-address">{station?.address || '地址未填写'}</View>
    </View>

    <View className="detail-section">
      <View className="detail-section-title">{"站点设备"}</View>
      {devices?.length > 0 && devices.map((device:any) => (<View
        className={`device-card${device?.clickable ? '' : ' device-card-offline'}`}
        data-id={device?.device_id}
        onClick={device?.clickable ? event("openDevice", {}) : undefined}
        key={String(device?.device_id)}>
        <View className="device-main">
          <Text className="device-id">{device?.device_id || '未知设备'}</Text>
          <View className="device-status">
            <View className={`device-dot${device?.online ? ' device-dot-online' : ''}`} />
            <Text className="device-status-text">{device?.statusText}</Text>
          </View>
        </View>
        <Text className="device-heartbeat">{device?.lastHeartbeatText}</Text>
      </View>))}
      {!!(!devices?.length && !loading && !error) && (<View className="detail-hint">{"该站点暂无设备记录。"}</View>)}
    </View>
  </>)}
</View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
