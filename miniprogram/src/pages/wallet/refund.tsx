import { Fragment } from "react";
import { Button, Input, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./refund.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { items, total, page, amount, reason, loading, refunding, pendingRetry, error, notice, needsLogin } = data;
return (<><View className="container">{!!(needsLogin) && (<Button onClick={event("login", {})}>{"登录查看退款"}</Button>)}
{!!(!needsLogin) && (<View className="card"><View>{"申请原路退款"}</View>
<Input aria-label="退款金额" type="digit" placeholder="金额（元）" value={amount} onInput={event("inputAmount", {})} disabled={pendingRetry || refunding}></Input>
<Input aria-label="退款原因" placeholder="退款原因（可选）" maxlength={255} value={reason} onInput={event("inputReason", {})} disabled={pendingRetry || refunding}></Input>
{!!(pendingRetry) && (<View>{"上次申请结果尚未确认，重试将沿用同一申请，不会重复预留余额。"}</View>)}
<Button onClick={event("submit", {})} loading={refunding} disabled={loading || refunding}>{pendingRetry ? '重试同一申请' : '申请退款'}</Button></View>)}
{!!(notice) && (<View className="card">{notice}</View>)}
{!!(error) && (<View className="card">{error}
<Button onClick={event("load", {})}>{"刷新记录"}</Button></View>)}
{!!(loading) && (<View>{"正在读取退款进度…"}</View>)}
{(items || []).map((item: any, index: number) => (<View className="card" key={String(item?.request_id ?? index)}><View>{item?.amountText}
{" · "}
{item?.statusText}</View>
<View>{"已退款 "}
{item?.refundedText}</View>
<View className="text-secondary">{item?.created_at}</View>
{!!(item?.review) && (<View>{"审核意见："}
{item?.review?.comment}</View>)}
{(item?.refund_orders || []).map((part: any, index: number) => (<View key={String(part?.refund_no ?? index)}><View>{part?.amountText}
{" · "}
{part?.statusText}</View>
<View className="text-secondary">{part?.refund_no}</View>
{!!(part?.failure_reason) && (<View>{part?.failure_reason}</View>)}</View>))}</View>))}
{!!(!loading && !items?.length && !needsLogin) && (<View>{"暂无退款申请"}</View>)}</View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
