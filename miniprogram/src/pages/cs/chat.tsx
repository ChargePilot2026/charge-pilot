import { Fragment } from "react";
import { Button, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./chat.controller";
import "./chat.css";

export default function PageView() {
const { data, event } = useController(controller);
const { scene, entry, loading, error, needsLogin, opening } = data;
return (<><View className="container">{!!(needsLogin) && (<View className="card"><View>{"登录后联系在线客服"}</View>
<Button type="primary" onClick={event("login", {})}>{"微信登录"}</Button></View>)}
{!!(!(needsLogin) && (loading)) && (<View className="card">{"正在查找可用客服…"}</View>)}
{!!(entry) && (<View className="card"><View className="heading">{entry?.agent_name || 'ChargePilot 客服'}</View>
<View className="text-secondary">{"咨询主题："}
{scene === 'refund' ? '退款' : scene === 'complaint' ? '投诉' : '一般咨询'}</View>
{!!(entry?.available) && (<Button type="primary" onClick={event("openChat", {})} loading={opening} disabled={opening}>{"打开微信客服会话"}</Button>)}
{!!(!(entry?.available)) && (<View className="notice">{"在线会话尚未完成配置，请复制客服微信号或稍后再试。"}</View>)}
{!!(entry?.agent_wechat) && (<Button onClick={event("copyWechat", {})}>{"复制客服微信号 "}
{entry?.agent_wechat}</Button>)}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(!loading && !error && !entry && !needsLogin) && (<View className="card">{"当前没有可用客服，请稍后重试。"}</View>)}
<View className="card text-secondary">{"如反馈订单问题，请准备订单号；不要在客服消息中发送密码或支付验证码。"}</View></View></>);
}
