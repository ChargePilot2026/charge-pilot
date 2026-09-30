import { Fragment } from "react";
import { Button, Picker, Text, Textarea, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./fault.controller";
import "./fault.css";

export default function PageView() {
const { data, event } = useController(controller);
const { deviceId, faultTypeIndex, faultTypes, description, submitting, needsLogin, error, historyError, notice, reports, reportsPage, reportsHasMore, loadingReports, expandedReport, reportHistory, reportHistoryLoading, reportHistoryError } = data;
return (<><View className="container">{!!(needsLogin) && (<View className="card"><View>{"登录后提交设备报修"}</View>
<Button type="primary" onClick={event("login", {})}>{"微信登录"}</Button></View>)}
<View className="card"><View className="heading">{"设备报修"}</View>
<View className="device">{"设备编号："}
{deviceId || '未指定'}</View>
<Picker range={faultTypes} value={faultTypeIndex} onChange={event("onType", {})}><View className="field">{"故障类型："}
{faultTypes?.[faultTypeIndex]}
{" ▾"}</View></Picker>
<Textarea placeholder="描述故障现象、发生时间和已尝试的处理方式" maxlength={2000} value={description} onInput={event("onDescription", {})} autoHeight={true}></Textarea>
<View className="text-secondary">{"请勿填写个人密码或支付信息。"}</View>
<Button type="primary" onClick={event("submit", {})} loading={submitting} disabled={submitting}>{"提交报修"}</Button></View>
{!!(notice) && (<View className="card success">{notice}</View>)}
{!!(error) && (<View className="card error"><View>{error}</View>
<Button onClick={event("submit", {})} disabled={submitting}>{"重试"}</Button></View>)}
<View className="card"><View className="heading">{"我的报修"}</View>
{(reports || []).map((item: any, index: number) => (<View className="report-row" key={String(item?.report_id ?? index)}><View className="report-head"><Text>{item?.device_id}</Text>
<Text className="status">{item?.statusText}</Text></View>
<View className="text-secondary">{"报修单 "}
{item?.report_id}
{" · "}
{item?.createdText}</View>
{!!(item?.description) && (<View className="report-description">{item?.description}</View>)}
<Button className="history-toggle" size="mini" data-report-id={item?.report_id} onClick={event("toggleHistory", {"report-id":item?.report_id})}>{expandedReport === item?.report_id ? '收起处理记录' : '查看处理记录'}</Button>
{!!(expandedReport === item?.report_id) && (<View className="history-box">{!!(reportHistoryLoading) && (<View className="text-secondary">{"正在读取处理记录…"}</View>)}
{(reportHistory || []).map((item: any, index: number) => (<View className="history-row" key={String(item?.event_id ?? index)}><View>{item?.eventText}
{" · "}
{item?.createdText}</View>
{!!(item?.fromStatusText || item?.toStatusText) && (<View className="text-secondary">{item?.fromStatusText || '—'}
{" → "}
{item?.toStatusText || '—'}</View>)}
{!!(item?.note) && (<View className="history-note">{item?.note}</View>)}</View>))}
{!!(reportHistoryError) && (<View className="error"><View>{reportHistoryError}</View>
<Button size="mini" data-report-id={item?.report_id} onClick={event("retryReportHistory", {"report-id":item?.report_id})}>{"重试"}</Button></View>)}
{!!(!reportHistoryLoading && !reportHistoryError && reportHistory?.length === 0) && (<View className="text-secondary">{"暂无处理记录"}</View>)}</View>)}</View>))}
{!!(reports?.length === 0 && !loadingReports) && (<View className="text-secondary">{"暂无报修记录"}</View>)}
{!!(historyError) && (<View className="error"><View>{historyError}</View>
<Button onClick={event("retryReports", {})} loading={loadingReports}>{"重试读取"}</Button></View>)}
{!!(reportsHasMore) && (<Button onClick={event("loadMoreReports", {})} loading={loadingReports}>{"加载更多"}</Button>)}
{!!(!(reportsHasMore) && (loadingReports)) && (<View className="text-secondary">{"正在读取报修进度…"}</View>)}</View></View></>);
}
