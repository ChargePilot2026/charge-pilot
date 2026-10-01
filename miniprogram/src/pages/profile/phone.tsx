import { Fragment } from "react";
import { Button, Input, View } from "@tarojs/components";
import { useController, developmentLogin, isH5 } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./phone.controller";
import "./phone.css";

export default function PageView() {
const { data, event } = useController(controller);
const { loading, busy, bound, needsLogin, error, notice, phone } = data;
return (<><View className="container">{!!(needsLogin) && (<View className="card"><View>{"登录后可管理手机号绑定"}</View>
<Button type="primary" onClick={event("login", {})}>{"微信登录"}</Button></View>)}
{!!(!(needsLogin) && (loading)) && (<View className="card">{"正在读取绑定状态…"}</View>)}
{!!(!(needsLogin || loading)) && (<View className="card"><View className="heading">{"手机号绑定"}</View>
<View className="text-secondary">{developmentLogin ? '开发环境可填写测试手机号。' : '通过微信授权绑定手机号。'}</View>
<View className="status">{bound ? '当前账号已绑定手机号' : '当前账号尚未绑定手机号'}</View>
{!bound && developmentLogin && <><Input placeholder="测试手机号" type="number" maxlength={11} value={phone} onInput={event('inputPhone',{})} /><Button type="primary" onClick={event('bindDevelopment',{})} loading={busy} disabled={busy}>绑定测试手机号</Button></>}
{!bound && !developmentLogin && !isH5 && (<Button type="primary" openType="getPhoneNumber" onGetPhoneNumber={event("onGetPhoneNumber", {})} loading={busy} disabled={busy}>{"授权并绑定手机号"}</Button>)}
{!bound && !developmentLogin && isH5 && <View>请在微信小程序内授权绑定手机号。</View>}
{!!(bound) && (<Button onClick={event("unbind", {})} loading={busy} disabled={busy}>{"解除绑定"}</Button>)}</View>)}
{!!(notice) && (<View className="card success">{notice}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
{!!(!needsLogin) && (<Button onClick={event("load", {})} disabled={busy}>{"刷新状态"}</Button>)}</View>)}</View></>);
}
