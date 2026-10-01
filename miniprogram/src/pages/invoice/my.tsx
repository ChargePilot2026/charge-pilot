import { Fragment } from "react";
import { Button, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./my.controller";
import "./my.css";

export default function PageView() {
const { data, event } = useController(controller);
const { items, page, pageSize, loading, error, needsLogin } = data;
return (<><View className="container">{!!(needsLogin) && (<Button type="primary" onClick={event("login", {})}>{"登录查看发票"}</Button>)}
{!!(!(needsLogin) && (!needsLogin)) && (<Button type="primary" onClick={event("apply", {})}>{"申请发票"}</Button>)}
{(items || []).map((item: any, index: number) => (<View className="card" key={String(item?.invoice_no ?? index)}><View className="row"><View className="title">{item?.title}</View>
<View className="tag">{item?.statusText}</View></View>
<View>{"金额 ¥"}
{item?.amountText}
{" · "}
{item?.biz_type === 'charge' ? '充电订单' : item?.biz_type}</View>
<View className="text-secondary">{item?.invoice_no}
{" · "}
{item?.createdText}</View>
{!!(item?.reject_reason) && (<View className="error">{"拒绝原因："}
{item?.reject_reason}</View>)}
{!!(item?.invoice_url) && (<Button size="mini" data-url={item?.invoice_url} onClick={event("openInvoice", {"url":item?.invoice_url})}>{"查看发票"}</Button>)}</View>))}
{!!(loading) && (<View className="card">{"正在读取发票记录…"}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(!loading && !items?.length && !needsLogin && !error) && (<View className="card">{"暂无发票申请记录"}</View>)}
{!!(items?.length > 0 && items?.length === page * pageSize && !loading) && (<Button onClick={event("onReachBottom", {})}>{"加载更多"}</Button>)}</View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
