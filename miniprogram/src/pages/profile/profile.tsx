import { Fragment } from "react";
import { Button, Image, Text, View } from "@tarojs/components";
import { useController } from "../../runtime";
import { inlineStyle, StationMap } from "../../runtime/components";
import controller from "./profile.controller";
import { DevelopmentAccount } from '../../runtime/development';
import { developmentLogin } from '../../runtime/platform';

export default function PageView() {
const { data, event } = useController(controller);
const { profile, loading, error, needsLogin, loggingIn } = data;
return (<><View className="container"><DevelopmentAccount />{!!(needsLogin) && (<View className="card"><View>{"登录后查看余额、优惠券和充电记录"}</View>
<Button type="primary" onClick={event("login", {})} loading={loggingIn} disabled={loggingIn}>{developmentLogin ? '开发账号登录' : '微信登录'}</Button></View>)}
{!!(loading) && (<View className="card">{"正在读取个人资料…"}</View>)}
{!!(error) && (<View className="card"><Text>{error}</Text>
{!!(!needsLogin) && (<Button onClick={event("refresh", {})}>{"重试"}</Button>)}</View>)}
{!!(profile) && (<View className="card"><View style={inlineStyle({"display": "flex", "alignItems": "center"})}>{!!(profile?.avatar_url) && (<Image src={profile?.avatar_url} style={inlineStyle({"width": "96rpx", "height": "96rpx", "borderRadius": "50%"})}></Image>)}
{!!(!(profile?.avatar_url)) && (<View style={inlineStyle({"fontSize": "40px"})}>{"👤"}</View>)}
<View style={inlineStyle({"marginLeft": "24rpx"})}><View style={inlineStyle({"fontSize": "18px", "fontWeight": "500"})}>{profile?.nickname || '用户'}</View>
<View className="text-secondary">{"可用余额 ¥"}
{profile?.balanceText}</View></View></View>
{!!(profile?.wallet?.status === 'frozen') && (<View>{"钱包已冻结，请联系客服"}</View>)}
<View>{"冻结金额 ¥"}
{profile?.frozenText}</View>
<View>{"手机号："}
{profile?.phone_bound ? '已绑定' : '未绑定'}</View>
<View>{"可用优惠券："}
{profile?.coupon_unused_count}
{" 张"}</View>
<View>{profile?.membershipText}</View>
<View className="text-secondary">{"注册日期 "}
{profile?.registeredText}</View></View>)}
<View className="card"><View style={inlineStyle({"display": "flex", "justifyContent": "space-between", "padding": "16rpx 0"})} onClick={event("goWallet", {})}><Text>{"我的余额与充值"}</Text>
<Text className="text-secondary">{"›"}</Text></View>
<View style={inlineStyle({"display": "flex", "justifyContent": "space-between", "padding": "16rpx 0"})} onClick={event("goOrders", {})}><Text>{"我的订单"}</Text>
<Text className="text-secondary">{"›"}</Text></View>
<View style={inlineStyle({"display": "flex", "justifyContent": "space-between", "padding": "16rpx 0"})} onClick={event("goRefunds", {})}><Text>{"我的退款"}</Text>
<Text className="text-secondary">{"›"}</Text></View>
<View style={inlineStyle({"display": "flex", "justifyContent": "space-between", "padding": "16rpx 0"})} onClick={event("goCoupons", {})}><Text>{"🎫 我的优惠券"}</Text>
<Text className="text-secondary">{"›"}</Text></View>
<View style={inlineStyle({"display": "flex", "justifyContent": "space-between", "padding": "16rpx 0"})} onClick={event("goInvoices", {})}><Text>{"📄 我的发票"}</Text>
<Text className="text-secondary">{"›"}</Text></View>
<View style={inlineStyle({"display": "flex", "justifyContent": "space-between", "padding": "16rpx 0"})} onClick={event("goPhoneBind", {})}><Text>{"📱 绑定手机号"}</Text>
<Text className="text-secondary">{"›"}</Text></View>
<View style={inlineStyle({"display": "flex", "justifyContent": "space-between", "padding": "16rpx 0"})} onClick={event("goAnnouncements", {})}><Text>{"📢 公告"}</Text>
<Text className="text-secondary">{"›"}</Text></View>
<View style={inlineStyle({"display": "flex", "justifyContent": "space-between", "padding": "16rpx 0"})} onClick={event("goCustomerService", {})}><Text>{"💬 在线客服"}</Text>
<Text className="text-secondary">{"›"}</Text></View></View>
{!!(!needsLogin) && (<View className="card" onClick={event("onLogout", {})} style={inlineStyle({"textAlign": "center", "color": "#999"})}>{"退出登录"}</View>)}</View></>);
}
