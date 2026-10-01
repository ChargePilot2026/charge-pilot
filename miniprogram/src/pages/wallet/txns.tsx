import { Fragment } from "react";
import { Button, Picker, Text, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./txns.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { items, total, page, loading, error, needsLogin, statusIndex, statusNames, statusValues } = data;
return (<><View className="container">{!!(needsLogin) && (<View className="card"><Button type="primary" onClick={event("login", {})}>{"登录查看流水"}</Button></View>)}
<Picker range={statusNames} value={statusIndex} onChange={event("onStatus", {})}><View className="card">{"类型："}
{statusNames?.[statusIndex]}
{" ▾"}</View></Picker>
{(items || []).map((item: any, index: number) => (<View className="card" key={String(item?.txn_no ?? index)}><View>{item?.typeText}
{"　"}
{item?.amountText}
{" 元"}</View>
<View>{"变动后余额 ¥"}
{item?.balanceText}</View>
<View>{item?.remark}</View>
<View className="text-secondary">{item?.timeText}</View>
<View className="text-secondary">{"流水号 "}
{item?.txn_no}</View></View>))}
{!!(loading) && (<View className="card">{"正在读取流水…"}</View>)}
{!!(error) && (<View className="card"><Text>{error}</Text>
<Button onClick={event("retry", {})}>{"重试"}</Button></View>)}
{!!(!loading && !error && !needsLogin && total === 0) && (<View className="card">{"暂无资金流水"}</View>)}
{!!(items?.length < total && !loading) && (<Button onClick={event("onReachBottom", {})}>{"加载更多"}</Button>)}</View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
