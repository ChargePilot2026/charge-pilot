import { Fragment } from "react";
import { Button, Picker, Text, Textarea, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { HOME_TABS, TabBar } from "../../runtime/TabBar";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./detail.controller";

export default function PageView() {
const { data, event } = useController(controller);
const { order, loading, error, needsLogin, paying, paymentNotice, curve, curveLoading, curveError, feedbackRatingIndex, feedbackCategoryIndex, feedbackRatings, feedbackCategories, feedbackCategoryValues, feedbackContent, feedbackBusy, feedbackError, feedbackNotice } = data;
return (<><View className="container">{!!(needsLogin) && (<Button onClick={event("login", {})}>{"登录查看订单"}</Button>)}
{!!(loading) && (<View className="card">{"正在加载…"}</View>)}
{!!(error) && (<View className="card"><Text>{error}</Text>
<Button onClick={event("load", {})} disabled={loading}>{"重试"}</Button></View>)}
{!!(order) && (<Fragment>{!!(order?.status === 'pending_payment') && (<Button onClick={event("pay", {})} loading={paying} disabled={paying || loading}>{"继续付款"}</Button>)}
{!!(paymentNotice) && (<View className="card">{paymentNotice}</View>)}
<View className="card"><View>{order?.order_no}</View>
<View className="tag tag-blue">{order?.statusLabel}</View>
<View>{order?.station_name || order?.device_id}
{" · 端口 "}
{order?.port_no}</View>
<View>{"开始："}
{order?.startedText}</View>
<View>{"结束："}
{order?.endedText}</View>
{!!(order?.display?.show_energy) && (<View>{"电量："}
{order?.meter_kwh || '—'}
{" "}
{order?.display?.hide_unit ? '' : 'kWh'}</View>)}</View>
<View className="card">{!!(order?.display?.show_fee_split) && (<View>{"电费："}
{order?.electricText}</View>)}
{!!(order?.display?.show_fee_split) && (<View>{"服务费："}
{order?.serviceText}</View>)}
<View>{"总费用："}
{order?.totalText}</View>
<View>{"实付："}
{order?.paidText}</View></View>
<View className="card"><View>{"退款状态："}
{order?.refundLabel}</View>
<View>{"已退："}
{order?.refundedText}</View>
<View>{"支付单号："}
{order?.payment_order_no || '—'}</View></View>
{!!(order?.billing_status === 'manual_review') && (<View className="card">{"待核对：缺少可靠计量，管理员核对后结算退款。"}</View>)}
{!!(order?.display?.show_method && order?.offer) && (<View className="card">{order?.offer?.mode === 'amount' ? '金额' : order?.offer?.mode === 'duration' ? '时长' : '电量'}
{" · "}
{order?.offer?.name}</View>)}
{(order?.tariff_lines || []).map((item: any, index: number) => (<View className="card" key={String(item ?? index)}>{item}</View>))}
{!!(order?.rule_description) && (<View className="card">{order?.rule_description}</View>)}
{(order?.card_operations || []).map((item: any, index: number) => (<View className="card" key={String(item?.operation_id ?? index)}>{item?.kind === 'extend' ? '加时' : '刷卡启动'}
{" "}
{item?.minutes}
{" 分钟 · "}
{item?.status === 'confirming' ? '确认中' : item?.status === 'confirmed' ? '已确认' : '明确失败，已补偿退款'}</View>))}
{!!(order?.failure_reason) && (<View className="card">{"异常原因："}
{order?.failure_reason}</View>)}
{!!(['completed','refunding','refunded'].includes(order?.status)) && (<View className="card"><Button onClick={event("loadCurve", {})} loading={curveLoading} disabled={curveLoading}>{curve ? '刷新历史曲线' : '查看历史曲线'}</Button>
{!!(curveError) && (<View className="error">{curveError}</View>)}
{!!(curve) && (<View>{!!(!curve?.series?.length) && (<View className="text-secondary">{"没有可显示的聚合遥测。"}</View>)}
{(curve?.series || []).map((item: any, index: number) => (<View className="curve-row" key={String(item?.bucket_start ?? index)}><View>{item?.bucketText}</View>
<View>{!!(order?.display?.show_power) && (<Text>{"功率 "}
{item?.power_w_avg || '—'}
{" "}
{order?.display?.hide_unit ? '' : 'W'}
{" ·"}</Text>)}
{"温度 "}
{item?.temperature_c_avg || '—'}
{" °C"}</View>
<View>{"电压 "}
{item?.voltage_v_avg || '—'}
{" V · 电流 "}
{item?.current_a_avg || '—'}
{" A"}</View></View>))}
{!!(order?.display?.show_energy && curve?.summary?.total_kwh) && (<View>{"总电量："}
{curve?.summary?.total_kwh}
{" kWh"}</View>)}</View>)}</View>)}
{!!(['completed','refunding','refunded'].includes(order?.status) && !order?.feedback_submitted) && (<View className="card"><View>{"订单评价与反馈"}</View>
<Picker range={feedbackRatings} value={feedbackRatingIndex} onChange={event("onFeedbackRating", {})}><View className="field">{"评分："}
{feedbackRatings?.[feedbackRatingIndex]}
{" ▾"}</View></Picker>
<Picker range={feedbackCategories} value={feedbackCategoryIndex} onChange={event("onFeedbackCategory", {})}><View className="field">{"类型："}
{feedbackCategories?.[feedbackCategoryIndex]}
{" ▾"}</View></Picker>
<Textarea placeholder="说说充电体验或需要跟进的问题（选填）" maxlength={2000} value={feedbackContent} onInput={event("onFeedbackContent", {})} autoHeight={true}></Textarea>
{!!(feedbackError) && (<View className="error">{feedbackError}</View>)}
{!!(feedbackNotice) && (<View className="success">{feedbackNotice}</View>)}
<Button type="primary" onClick={event("submitFeedback", {})} loading={feedbackBusy} disabled={feedbackBusy}>{"提交反馈"}</Button></View>)}
{!!(order?.feedback_submitted) && (<View className="card success">{"已收到本订单反馈，谢谢。"}</View>)}
{!!(order?.device_id) && (<View className="card"><Button onClick={event("reportFault", {})}>{"设备报修"}</Button>
</View>)}</Fragment>)}</View>
<TabBar active="home" tabs={HOME_TABS} /></>);
}
