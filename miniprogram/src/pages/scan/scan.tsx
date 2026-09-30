import { Fragment } from "react";
import { Button, Input, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./scan.controller";
import { isH5, developmentLogin } from '../../runtime/platform';

export default function PageView() {
const { data, event, page } = useController(controller);
const { busy, error, manualCode } = data;
return (<><View className="container"><View className="card"><View>{"请扫描充电设备或端口上的二维码"}</View>
{!isH5 && <Button onClick={event("scan", {})} loading={busy} disabled={busy}>{"打开相机扫码"}</Button>}
{isH5 && <View className='text-secondary'>浏览器验证请粘贴二维码内容，或输入设备编号 / 端口码。</View>}</View>
<View className="card"><View>{"也可以输入设备编号或端口码"}</View>
<Input aria-label="设备编号或端口码" placeholder="请输入设备编号或端口码" maxlength={64} value={manualCode} onInput={event("inputCode", {})} onConfirm={event("manual", {})}></Input>
<Button onClick={event("manual", {})} disabled={busy || !manualCode}>{"查询"}</Button></View>
{developmentLogin && <View className='card'><View>双模拟器快捷入口（先在后台创建对应设备）</View>{['5348240514082652','5348240514082653'].map((id, i) => <Button key={id} onClick={() => { page.setData({ manualCode: id }); page.manual() }} disabled={busy}>模拟器 {i + 1} · {id}</Button>)}</View>}
{!!(error) && (<View className="card">{error}</View>)}</View></>);
}
