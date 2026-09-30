import { Fragment } from "react";
import { Button, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./cards.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { cards, loading, error, needsLogin, busy } = data;
return (<><View className="container">{!!(needsLogin) && (<Button onClick={event("login", {})}>{"登录查看在线卡"}</Button>)}
<View className="card">{"在线卡共用你的钱包余额。首次刷卡启动，同一卡再次刷卡追加冻结套餐的时长；扣款和退款可在资金流水查看。"}</View>
{!!(loading) && (<View>{"读取中…"}</View>)}
{!!(error) && (<View className="card">{error}
<Button onClick={event("load", {})}>{"重试"}</Button></View>)}
{!!(!loading && !needsLogin && !cards?.length) && (<View className="card">{"尚无绑定卡，请联系管理员核验绑定。"}</View>)}
{(cards || []).map((item: any, index: number) => (<View className="card" key={String(item?.id ?? index)}><View>{item?.card_no}
{" · "}
{item?.label}</View>
{!!(item?.status === 'active') && (<Button data-card={item?.card_no} data-status="lost" onClick={event("change", {"card":item?.card_no,"status":"lost"})} disabled={busy}>{"挂失"}</Button>)}
<Button data-card={item?.card_no} data-status="unbound" onClick={event("change", {"card":item?.card_no,"status":"unbound"})} disabled={busy}>{"解绑"}</Button></View>))}</View></>);
}
