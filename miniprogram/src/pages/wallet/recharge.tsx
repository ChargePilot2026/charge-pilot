import { Fragment } from "react";
import { Button, Input, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./recharge.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { amount, items, page, hasMore, loading, paying, pending, error, notice, needsLogin } = data;
return (<><View className="container">{!!(needsLogin) && (<View className="card"><Button onClick={event("login", {})}>{"微信登录"}</Button></View>)}
{!!(!(needsLogin)) && (<View className="card"><View>{"钱包充值（元）"}</View>
<Input type="digit" value={amount} disabled={pending || paying} onInput={event("inputAmount", {})} placeholder="请输入充值金额"></Input>
<Button onClick={event("submit", {})} loading={paying} disabled={paying || loading}>{pending ? '继续原充值单 / 核实结果' : '微信支付充值'}</Button>
<View>{"到账以服务端支付结果为准，取消或网络中断后可继续同一笔充值。"}</View></View>)}
{!!(error) && (<View className="card">{error}</View>)}
{!!(notice) && (<View className="card">{notice}</View>)}
<Button onClick={event("refresh", {})} disabled={paying || loading}>{"刷新充值记录"}</Button>
{(items || []).map((item: any, index: number) => (<View className="card" key={String(item?.request_id ?? index)}><View>{item?.pay_order_no}</View>
<View>{"¥"}
{item?.amountText}
{" · "}
{item?.statusText}</View>
{!!(item?.can_pay) && (<Button onClick={event("resume", {"id":item?.request_id})} data-id={item?.request_id} disabled={paying || pending}>{"继续支付"}</Button>)}</View>))}
{!!(hasMore) && (<Button onClick={event("more", {})} disabled={loading || paying}>{"加载更多"}</Button>)}</View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
