import { Fragment } from "react";
import { Button, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./detail.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { station, loading, error, needsLogin } = data;
return (<><View className="container">{!!(loading) && (<View className="card">{"正在加载…"}</View>)}
{!!(error) && (<View className="card">{error}
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(station) && (<View className="card"><View>{station?.name}</View>
<View>{station?.address || '地址未填写'}</View>
<Button onClick={event("navigate", {})}>{"地图导航"}</Button>
{!!(station?.contact_phone) && (<Button onClick={event("call", {})}>{"联系站点"}</Button>)}</View>)}</View></>);
}
