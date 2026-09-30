import { Fragment } from "react";
import { Button, Input, Picker, View } from "@tarojs/components";
import { useController, isH5 } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./nearby.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { items, markers, latitude, longitude, loading, error, radiusIndex, radii, needsLogin } = data;
return (<><View className="container"><Button onClick={event("locate", {})} disabled={loading}>{"获取当前位置"}</Button>
{isH5 && <View className="card"><View>浏览器定位不可用时，可按站点坐标搜索</View><Input placeholder="经度，例如 116.397" value={data.longitudeInput || ''} onInput={event('inputLongitude',{})} /><Input placeholder="纬度，例如 39.908" value={data.latitudeInput || ''} onInput={event('inputLatitude',{})} /><Button onClick={event('useCoordinates',{})} disabled={loading}>按坐标查询</Button></View>}
<Picker range={radii} value={radiusIndex} onChange={event("onRadius", {})}><View className="card">{"搜索半径："}
{radii?.[radiusIndex]}
{" 公里 ▾"}</View></Picker>
{!!(latitude != null) && (<StationMap latitude={latitude} longitude={longitude} markers={markers} showLocation={true} onMarkerTap={event("open", {})} style={inlineStyle({"width": "100%", "height": "400rpx"})}></StationMap>)}
{!!(error) && (<View className="card">{error}
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(loading) && (<View className="card">{"正在查询…"}</View>)}
{(items || []).map((item: any, index: number) => (<View className="card" data-id={item?.id} onClick={event("open", {"id":item?.id})} key={String(item?.id ?? index)}><View>{item?.name}
{" · "}
{item?.distanceText}</View>
<View className="text-secondary">{item?.address || '地址未填写'}</View></View>))}
{!!(latitude != null && !loading && !error && !items?.length) && (<View className="card">{"该范围内暂无开放站点，可扩大搜索范围。"}</View>)}</View></>);
}
