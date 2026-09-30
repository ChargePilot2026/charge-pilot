import { Fragment } from "react";
import { Button, Text, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./charging.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { snapshot, loading, error, needsLogin, stopping, stopNotice, curve, curveLoading, curveError } = data;
return (<><View className="container">{!!(needsLogin) && (<Button onClick={event("login", {})}>{"登录查看充电状态"}</Button>)}
{!!(loading && !snapshot) && (<View className="card">{"正在读取订单状态…"}</View>)}
{!!(error) && (<View className="card"><View>{error}</View>
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(snapshot) && (<Fragment><View className="card"><View className="tag tag-blue">{snapshot?.statusLabel}</View>
<View>{snapshot?.order_no}</View>
{!!(snapshot?.status === 'paid') && (<View>{"支付已确认，启动确认中；系统查询并重放同一个启动操作，请勿重复下单。"}</View>)}
{!!(snapshot?.status === 'pending_payment') && (<View>{"订单等待支付确认，设备尚未开始充电。"}</View>)}
{!!(!snapshot?.poll_continue) && (<View>{"订单已结束，状态刷新已停止。"}</View>)}</View>
{!!(snapshot?.status === 'charging') && (<View className="card">{!!(!snapshot?.telemetry_available) && (<View className="text-secondary">{"暂未收到实时遥测数据。"}</View>)}
{!!(snapshot?.display?.show_power) && (<View>{"功率："}
{snapshot?.powerText}
{" "}
{snapshot?.display?.hide_unit ? '' : 'W'}</View>)}
<View>{"电压："}
{snapshot?.voltageText}
{" V"}</View>
<View>{"温度："}
{snapshot?.temperatureText}
{" °C"}</View>
<Button onClick={event("loadCurve", {})} loading={curveLoading} disabled={curveLoading}>{curve ? '刷新近 30 分钟曲线' : '查看近 30 分钟曲线'}</Button></View>)}
{!!(curveError) && (<View className="card"><View>{curveError}</View>
<Button onClick={event("loadCurve", {})} disabled={curveLoading}>{"重试"}</Button></View>)}
{!!(curve) && (<View className="card"><View>{"遥测曲线（采样间隔 "}
{curve?.sample_interval_seconds}
{" 秒）"}</View>
{!!(!curve?.series?.length) && (<View className="text-secondary">{"该时间段没有设备遥测。"}</View>)}
{(curve?.series || []).map((item: any, index: number) => (<View className="curve-row" key={String(item?.ts ?? index)}><View>{item?.tsText}
{!!(snapshot?.display?.show_power) && (<Text>{"· "}
{item?.powerText}
{" "}
{snapshot?.display?.hide_unit ? '' : 'W'}</Text>)}
{"· "}
{item?.currentText}
{" A · "}
{item?.voltageText}
{" V · "}
{item?.temperatureText}
{" °C"}</View></View>))}</View>)}
<View className="card">{!!(snapshot?.display?.show_energy) && (<View>{"累计电量："}
{snapshot?.energyText}
{" "}
{snapshot?.display?.hide_unit ? '' : 'kWh'}</View>)}
<View>{"充电时长："}
{snapshot?.durationText}</View>
<View>{"费用："}
{snapshot?.feeText}</View></View>
{!!(snapshot?.billing_status === 'manual_review') && (<View className="card">{"计量不足，待管理员核对。暂不猜算收费。"}</View>)}
{(snapshot?.card_operations || []).map((item: any, index: number) => (<View className="card" key={String(item?.operation_id ?? index)}>{item?.kind === 'extend' ? '加时' : '刷卡启动'}
{" "}
{item?.minutes}
{" 分钟 · "}
{item?.status === 'confirming' ? '确认中' : item?.status === 'confirmed' ? '已确认' : '失败，已补偿退款'}</View>))}
{(snapshot?.tariff_lines || []).map((item: any, index: number) => (<View className="card" key={String(item ?? index)}>{item}</View>))}
{!!(snapshot?.rule_description) && (<View className="card">{snapshot?.rule_description}</View>)}
<Button onClick={event("detail", {})}>{"查看订单详情"}</Button>
{!!(snapshot?.status === 'charging') && (<Button onClick={event("stopCharge", {})} loading={stopping} disabled={stopping || loading}>{"停止充电"}</Button>)}
{!!(stopNotice) && (<View className="card">{stopNotice}</View>)}</Fragment>)}</View></>);
}
