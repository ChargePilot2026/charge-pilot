import { Fragment } from "react";
import { Button, Text, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./index.controller";
import "./index.css";

export default function PageView() {
const { data, event } = useController(controller);
const { ongoing, announcements, latitude, longitude, stations, markers, mapError } = data;
return (<><View className="container">{!!(announcements?.length > 0) && (<View className="card" onClick={event("goAnnouncements", {})}><View className="tag tag-orange">{"公告"}</View>
<Text className="text-secondary" style={inlineStyle({"marginLeft": "8rpx"})}>{announcements?.[0]?.title}</Text></View>)}
<View className="card"><View>{"附近充电站"}</View>
{!!(latitude !== null) && (<StationMap latitude={latitude} longitude={longitude} markers={markers} showLocation={true} onMarkerTap={event("goStation", {})} style={inlineStyle({"width": "100%", "height": "400rpx"})}></StationMap>)}
{!!(mapError) && (<View className="text-secondary">{mapError}</View>)}
<Button onClick={event("goMap", {})}>{"查看地图与站点"}</Button></View>
{!!(ongoing) && (<View className="card" onClick={event("goOngoing", {})}><View style={inlineStyle({"display": "flex", "justifyContent": "space-between"})}><View><View className="text-secondary">{ongoing?.status === 'pending_payment' ? '待支付' : ongoing?.status === 'paid' ? '等待设备启动' : '充电中'}</View>
<View style={inlineStyle({"fontSize": "18px", "fontWeight": "500"})}>{"订单 "}
{ongoing?.order_no}</View></View>
<View className="tag tag-green">{"查看"}</View></View></View>)}
<View className="card" style={inlineStyle({"textAlign": "center", "padding": "80rpx 0"})}><View style={inlineStyle({"fontSize": "64rpx"})}>{"⚡"}</View>
<View style={inlineStyle({"margin": "16rpx 0"})}>{"扫码充电"}</View>
<View className="btn-primary" style={inlineStyle({"margin": "32rpx 80rpx 0"})} onClick={event("onScanTap", {})}>{"扫一扫"}</View>
<View className="text-secondary" style={inlineStyle({"marginTop": "16rpx"})}>{"对准充电桩上的二维码"}</View></View>
<View className="card"><View style={inlineStyle({"display": "flex", "justifyContent": "space-around"})}><View onClick={event("goAnnouncements", {})}><View style={inlineStyle({"fontSize": "40rpx"})}>{"📢"}</View>
<View className="text-secondary">{"公告"}</View></View>
{!!(ongoing) && (<View onClick={event("goOngoing", {})}><View style={inlineStyle({"fontSize": "40rpx"})}>{"🔋"}</View>
<View className="text-secondary">{"充电中"}</View></View>)}
<View onClick={event("goProfile", {})}><View style={inlineStyle({"fontSize": "40rpx"})}>{"👤"}</View>
<View className="text-secondary">{"我的"}</View></View></View></View></View></>);
}
