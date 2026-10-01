import { Fragment } from "react";
import { Button, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./scan-result.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { deviceId, deviceStatus, deviceStatusLabel, deviceNotice, ports, selected, offers, selectedOffer, loading, error, paying, paymentNotice, paymentNo, startAttempted, canRetryPayment, feeText, display, stopWhenFull } = data;
return (<><View className="container">{!!(loading) && (<View className="card">{"正在读取端口信息…"}</View>)}
{!!(error) && (<View className="card"><View>{error}</View>
<Button onClick={event("load", {})} disabled={loading || paying || startAttempted}>{"重试"}</Button></View>)}
{!!(deviceId) && (<View className="card"><View>{"设备 "}
{deviceId}
{" · "}
{deviceStatusLabel}</View>
{!!(deviceNotice) && (<View>{deviceNotice}</View>)}
{!!(!(deviceNotice)) && (<View className="text-secondary">{"请核对插座编号后选择端口"}</View>)}
{!!(!ports?.length) && (<View>{"该设备暂无可用端口信息"}</View>)}</View>)}
{(ports || []).map((item: any, index: number) => (<View className="card" key={String(item?.port_id ?? index)}><View>{"端口 "}
{item?.port_no}
{" · "}
{item?.statusLabel}</View>
<View className="text-secondary">{item?.port_code}</View>
<Button data-id={item?.port_id} onClick={event("selectPort", {"id":item?.port_id})} disabled={loading || paying || startAttempted || !item?.selectable}>{item?.selectable ? '选择此端口' : '当前不可选择'}</Button></View>))}
{!!(selected) && (<View className="card"><View>{"已选端口 "}
{selected?.port_no}
{" · "}
{selected?.statusLabel}</View>
{!!(selected?.selectable) && (<Fragment><View>{"选择充电套餐"}</View>
{!!(stopWhenFull) && (<View>{"支持满充停止，按实际消费退款。"}</View>)}
{!!(!loading && !offers?.length) && (<View>{"该站点尚未发布可用方案"}</View>)}
{(offers || []).map((item: any, index: number) => (<View className="card" key={String(item?.id ?? index)}><View>{item?.modeLabel}
{" · "}
{item?.name}
{" · "}
{item?.priceText}</View>
<View>{item?.ruleText}</View>
{!!(display?.fee_split_inline) && (<View>{item?.mode === 'duration' ? '支付价格为时长套餐费' : item?.mode === 'energy' ? '支付价格由冻结电费与服务费单价计算' : '按冻结电费与服务费结算，实收不超过预付金额'}</View>)}
<Button data-id={item?.id} onClick={event("selectOffer", {"id":item?.id})} disabled={paying || startAttempted}>{selectedOffer?.id === item?.id ? '已选择' : '选择'}</Button></View>))}
{!!(selectedOffer && (!startAttempted || canRetryPayment)) && (<Button onClick={event("pay", {})} loading={paying} disabled={loading || paying}>{"支付 "}
{selectedOffer?.priceText}
{" 并充电"}</Button>)}</Fragment>)}
{!!(!(selected?.selectable)) && (<View>{"此端口当前不可充电，请选择其他空闲端口。"}</View>)}</View>)}
{!!(paymentNotice) && (<View className="card">{paymentNotice}</View>)}
{!!(startAttempted) && (<View className="card">{!!(paymentNo) && (<View>{"支付单号 "}
{paymentNo}
{" · "}
{feeText}</View>)}
<Button onClick={event("history", {})}>{"查看订单记录"}</Button></View>)}
<Button onClick={event("rescan", {})} disabled={paying || startAttempted}>{"重新扫码"}</Button></View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
