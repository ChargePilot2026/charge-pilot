import { Fragment } from "react";
import { Button, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./list.controller";
import "./list.css";

export default function PageView() {
const { data, event } = useController(controller);
const { items, loading, error, needsLogin } = data;
return (<><View className="container">{(items || []).map((item: any, index: number) => (<View className="card" key={String(item?.id ?? index)}><View className="title">{item?.title}</View>
<View className="text-secondary">{item?.timeText}</View>
<View className="content">{item?.content}</View></View>))}
{!!(loading) && (<View className="card">{"正在读取公告…"}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(!loading && !error && !items?.length && !needsLogin) && (<View className="card">{"暂无生效公告"}</View>)}</View></>);
}
