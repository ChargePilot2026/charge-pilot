import { Fragment } from "react";
import { Button, Input, Picker, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./apply.controller";
import "./apply.css";

export default function PageView() {
const { data, event } = useController(controller);
const { orders, orderLabels, orderIndex, selected, invoiceTypeIndex, invoiceTypes, title, taxNo, email, loading, loadingDetail, submitting, needsLogin, error, notice } = data;
return (<><View className="container">{!!(needsLogin) && (<View className="card"><View>{"登录后申请发票"}</View>
<Button type="primary" onClick={event("login", {})}>{"微信登录"}</Button></View>)}
{!!(!(needsLogin)) && (<View className="card"><View className="heading">{"申请充电订单发票"}</View>
<Picker range={orderLabels} value={orderIndex} onChange={event("onOrderSelect", {})}><View className="field">{orderIndex >= 0 ? orderLabels?.[orderIndex] : '选择已完成的充电订单'}</View></Picker>
{!!(loadingDetail) && (<View className="text-secondary">{"正在核对实付金额…"}</View>)}
{!!(selected) && (<View className="amount">{"订单实付金额：¥"}
{selected?.amountText}</View>)}
<Picker range={invoiceTypes} value={invoiceTypeIndex} onChange={event("onType", {})}><View className="field">{"发票类型："}
{invoiceTypes?.[invoiceTypeIndex]}
{" ▾"}</View></Picker>
<Input placeholder="发票抬头" maxlength={128} value={title} onInput={event("onTitle", {})}></Input>
{!!(invoiceTypeIndex === 1) && (<Input placeholder="纳税人识别号" maxlength={32} value={taxNo} onInput={event("onTaxNo", {})}></Input>)}
<Input placeholder="接收邮箱（选填）" maxlength={128} value={email} onInput={event("onEmail", {})}></Input>
<Button type="primary" onClick={event("submit", {})} loading={submitting} disabled={submitting || loadingDetail || !selected}>{"提交申请"}</Button></View>)}
{!!(loading) && (<View className="card">{"正在读取已完成订单…"}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button onClick={event("loadOrders", {})} disabled={loading || submitting}>{"重试"}</Button></View>)}
{!!(notice) && (<View className="card success">{notice}</View>)}
{!!(!loading && !needsLogin && !orders?.length) && (<View className="card">{"没有可申请开票的已完成订单"}</View>)}</View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
