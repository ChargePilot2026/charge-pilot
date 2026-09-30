import { Fragment } from "react";
import { Button, Text, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./wallet.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { wallet, error, loading, needsLogin } = data;
return (<><View className="container">{!!(needsLogin) && (<View className="card"><Text>{"登录后查看钱包"}</Text>
<Button onClick={event("login", {})} type="primary">{"微信登录"}</Button></View>)}
{!!(loading) && (<View className="card">{"正在读取钱包…"}</View>)}
{!!(error) && (<View className="card"><Text>{error}</Text>
<Button onClick={event("load", {})}>{"重试"}</Button></View>)}
{!!(wallet) && (<View className="card"><View>{"可用余额"}</View>
<View style={inlineStyle({"fontSize": "32px"})}>{"¥"}
{wallet?.availableText}</View>
<View>{"冻结金额 ¥"}
{wallet?.frozenText}</View>
{!!(wallet?.status === 'frozen') && (<View>{"钱包已冻结，请联系客服"}</View>)}
<Button onClick={event("goRecharge", {})}>{"钱包充值"}</Button>
<Button onClick={event("goTxns", {})}>{"查看资金流水"}</Button></View>)}
{!!(wallet) && (<Button onClick={event("goCards", {})}>{"我的在线卡"}</Button>)}
{!!(wallet) && (<Button onClick={event("goRefund", {})}>{"申请退款 / 查看退款进度"}</Button>)}</View></>);
}
