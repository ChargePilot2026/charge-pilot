import { Alert, Button, Descriptions, Drawer, Space, Spin, Table, Tabs, Tag, Timeline, Tooltip, Typography } from 'antd';
import { CreditCardOutlined, QrcodeOutlined, WalletOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState, type ReactNode } from 'react';
import dayjs from 'dayjs';
import axios from 'axios';
import { adminSession, type ApiEnvelope, http } from '../../api/client';
import { LoadError } from '../../components/LoadError';
import { TABLE_PAGINATION } from '../../utils/tablePagination';
import { isChargeUserID } from '../../utils/chargeUserID';
import ManualRefund from '../ManualRefund';
import StationDetailsDrawer, { type StationReference } from '../stations/StationDetailsDrawer';
import type { StationRecord } from '../stations/StationWorkspace';
import OrderPackageDetails, { type SelectedPackage } from './OrderPackageDetails';
import OrderPowerCurve from './OrderPowerCurve';
import { businessStatusInfo, paymentStatusInfo, paymentStatusColors, chargingDuration, refundedAmount } from './presentation';

export interface Order {
  live?: { at: string; stale: boolean; kwh: number; seconds: number; fee?: { electric_cents: number; service_cents: number; total_cents: number }; fee_unavailable?: string };
  live_unavailable?: string;
  order_id: number;
  order_no: string;
  user_id: string;
  device_id: string;
  port_no: number;
  station_id: number | null;
  station_name: string | null;
  status: string;
  business_status: string;
  payment_status: string;
  payment_order_status?: string | null;
  start_source: 'payment' | 'balance' | 'card' | null;
  selected_scheme_name: string | null;
  selected_package_name: string | null;
  created_at: string;
  started_at: string | null;
  ended_at: string | null;
  duration_seconds: number | null;
  meter_kwh: string | null;
  electric_fee_cents: number | null;
  service_fee_cents: number | null;
  total_fee_cents: number | null;
  refund_status: string;
  refunded_cents: number | null;
  failure_reason?: string | null;
}
interface OrderTimeline { order_id: number; timeline: { event_id: string; at: string; event: string; actor: string; detail: string }[] }
interface OrderDetail extends Order {
  selected_package: SelectedPackage | null;
  refund_applicant_id: string | null;
  billing: { calculation_no: string | null; settlements: Settlement[] } | null;
  payment_order_id: number | null;
  payment_order_no: string | null;
  paid_cents: number | null;
  failure_reason: string | null;
}
interface Settlement {
  settlement_id: number; settlement_no: string; mode: string; status: string; split_pool_cents: number;
  parties: { party_id: number; party_code: string; party_name: string; ratio_bp: number; amount_cents: number; status: string }[];
}
const refunds: Record<string, string> = { none: '无退款', processing: '退款中', refunded: '已全额退款', partial_refunded: '部分退款' };
const businessStatusColors: Record<string, string> = { pending_start: '#ffa940', charging: '#4096ff' };
export const time = (value: string | null) => value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '—';
export const money = (value: number | null) => value == null ? '待结算' : `¥${(value / 100).toFixed(2)}`;
export const statusTag = (row: Order, kind: 'business' | 'payment') => {
  const info = kind === 'business' ? businessStatusInfo(row) : paymentStatusInfo(row);
  const color = kind === 'business' ? businessStatusColors[row.business_status] : paymentStatusColors[row.payment_status];
  const tag = <Tag style={color ? { color } : undefined}>{info.label}</Tag>;
  return info.hint ? <Tooltip title={info.hint}>{tag}</Tooltip> : tag;
};
export const orderDuration = (row: Order) => {
  const duration = chargingDuration(row);
  const hint = `${duration.hint || ''}${duration.sampledAt ? `采样时间：${time(duration.sampledAt)}。` : ''}`;
  return hint ? <Tooltip title={hint}><span>{duration.text}</span></Tooltip> : duration.text;
};
const liveHint = (row: Order) => row.live ? `${row.live.stale ? '读数已过期，保留最后采样值；' : ''}采样时间：${time(row.live.at)}。费用按订单冻结费率估算，结束后结算。` : row.live_unavailable || '等待设备计量';
export const orderMeter = (row: Order) => row.status === 'charging'
  ? <Tooltip title={liveHint(row)}><span>{row.live ? `${row.live.kwh.toFixed(3)}${row.live.stale ? '（旧）' : ''}` : '待上报'}</span></Tooltip>
  : row.meter_kwh ?? '—';
export const orderFee = (row: Order, kind: 'electric' | 'service' | 'total') => row.status === 'charging'
  ? <Tooltip title={`${liveHint(row)} ${row.live?.fee_unavailable || ''}`}><span>{row.live?.fee ? `≈${money(row.live.fee[`${kind}_cents`])}${row.live.stale ? '（旧）' : ''}` : '待计量'}</span></Tooltip>
  : money(row[`${kind}_fee_cents`]);
export function errorMessage(error: unknown): string {
  if (axios.isAxiosError<ApiEnvelope>(error)) {
    return error.response?.data?.message || '网络连接失败，请稍后重试';
  }
  return error instanceof Error ? error.message : '加载失败，请稍后重试';
}
export const startSources: Record<string, string> = { payment: '扫码支付', balance: '余额支付', card: '在线卡' };
const startSourceIcons = { payment: <QrcodeOutlined />, balance: <WalletOutlined />, card: <CreditCardOutlined /> };
export const startSourceTag = (source: Order['start_source']) => source && startSources[source]
  ? <Tag icon={startSourceIcons[source]}>{startSources[source]}</Tag> : '—';
export function cachedPermissions(): string[] {
  try {
    const permissions = JSON.parse(localStorage.getItem('cp_admin') || 'null')?.permissions;
    return Array.isArray(permissions) ? permissions.filter((permission): permission is string => typeof permission === 'string') : [];
  } catch { return []; }
}

export interface OrderDetailsDrawerProps {
  orderID: number | null;
  onClose: () => void;
  onChanged?: () => void;
  onUserSelect?: (userID: string) => void;
  children?: ReactNode;
}

export default function OrderDetailsDrawer(props: OrderDetailsDrawerProps) {
  const currentOrderID = useRef(props.orderID);
  currentOrderID.current = props.orderID;
  return <Drawer title="订单详情" open={props.orderID != null} onClose={props.onClose}
    width="min(1100px, 100vw)" destroyOnHidden>
    {props.orderID != null && <OrderDetailsContent key={props.orderID} {...props} orderID={props.orderID} currentOrderID={currentOrderID} />}
  </Drawer>;
}

function OrderDetailsContent({ orderID, onClose, onChanged, onUserSelect, children, currentOrderID }: OrderDetailsDrawerProps & {
  orderID: number;
  currentOrderID: { current: number | null };
}) {
  const [detail, setDetail] = useState<OrderDetail | null>(null);
  const [timeline, setTimeline] = useState<OrderTimeline | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(true);
  const [detailReload, setDetailReload] = useState(0);
  const [detailTab, setDetailTab] = useState('basic');
  const [permissions, setPermissions] = useState(cachedPermissions);
  const [orderStation, setOrderStation] = useState<StationReference | null>(null);
  const [expired, setExpired] = useState(false);
  const session = useRef(adminSession.epoch());
  const mounted = useRef(true);
  const request = useRef<AbortController>();
  const closeRef = useRef(onClose);
  const changedRef = useRef(onChanged);
  closeRef.current = onClose;
  changedRef.current = onChanged;
  const canReadChargeUsers = permissions.includes('charge_user.read');
  const canReadStations = permissions.includes('station.read');
  const isCurrent = () => mounted.current && currentOrderID.current === orderID && !expired && session.current === adminSession.epoch();

  useEffect(() => {
    mounted.current = true;
    const updateSession = () => {
      setPermissions(cachedPermissions());
      if (session.current === adminSession.epoch()) return;
      request.current?.abort();
      setExpired(true); setDetail(null); setTimeline(null); setOrderStation(null);
      closeRef.current();
    };
    window.addEventListener('cp-session', updateSession);
    window.addEventListener('storage', updateSession);
    return () => {
      mounted.current = false;
      request.current?.abort();
      window.removeEventListener('cp-session', updateSession);
      window.removeEventListener('storage', updateSession);
    };
  }, []);

  useEffect(() => {
    if (expired || session.current !== adminSession.epoch()) return;
    if (!Number.isSafeInteger(orderID) || orderID <= 0) {
      setDetailError('订单编号无效，请重新打开详情'); setDetailLoading(false);
      return;
    }
    const controller = new AbortController();
    request.current = controller;
    const requestConfig = { signal: controller.signal, cpEpoch: session.current };
    const current = () => isCurrent() && !controller.signal.aborted;
    if (!detail) setDetailLoading(true);
    setDetailError(null);
    Promise.all([
      http.get<ApiEnvelope<OrderDetail>>(`/api/v1/admin/orders/${orderID}`, requestConfig),
      http.get<ApiEnvelope<OrderTimeline>>(`/api/v1/admin/orders/${orderID}/timeline`, requestConfig),
    ]).then(([response, events]) => {
      if (!current()) return;
      const next = response.data.data, nextTimeline = events.data.data;
      if (!next || next.order_id !== orderID || !nextTimeline || nextTimeline.order_id !== orderID || !Array.isArray(nextTimeline.timeline)) {
        throw new Error('订单详情响应不完整，请重新读取');
      }
      setDetail(next); setTimeline(nextTimeline);
    }).catch(error => { if (current()) setDetailError(errorMessage(error)); })
      .finally(() => { if (current()) setDetailLoading(false); });
    return () => controller.abort();
  }, [orderID, detailReload, expired]);

  useEffect(() => {
    if (expired || detail?.status !== 'charging') return;
    const timer = window.setInterval(() => {
      if (isCurrent()) setDetailReload(value => value + 1);
    }, 5000);
    return () => window.clearInterval(timer);
  }, [detail?.status, expired]);

  const changed = () => {
    if (!isCurrent()) return;
    setDetailReload(value => value + 1);
    changedRef.current?.();
  };
  const stationLink = (row: Order) => {
    const name = row.station_name || (row.station_id ? `站点 #${row.station_id}` : '未关联站点');
    if (!canReadStations || !row.station_id || !Number.isSafeInteger(row.station_id) || row.station_id <= 0) return name;
    return <Button type="link" style={{ padding: 0, height: 'auto' }} aria-label={`查看${name}详情`}
      onClick={() => setOrderStation({ id: row.station_id!, name: row.station_name })}>{name}</Button>;
  };
  const stationSaved = (updated: StationRecord) => {
    if (!isCurrent()) return;
    setOrderStation(current => current?.id === updated.id ? updated : current);
    changed();
  };

  if (expired || currentOrderID.current !== orderID || session.current !== adminSession.epoch()) return null;
  return <>
      {detailLoading && <Spin />}
      {detailError && <LoadError title="详情加载失败" detail={detailError} onRetry={() => setDetailReload(value => value + 1)} />}
      {detail && <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <Space wrap><Typography.Text strong copyable>{detail.order_no}</Typography.Text>{statusTag(detail, 'business')}{statusTag(detail, 'payment')}</Space>
        {detail.failure_reason && <Alert type="warning" message="异常原因" description={detail.failure_reason} showIcon />}
        <Tabs key={detail.order_id} activeKey={detailTab} onChange={setDetailTab} style={{ width: '100%' }} items={[
          { key: 'basic', label: '基本信息', children: <Descriptions bordered column={{ xs: 1, sm: 2, md: 2, lg: 2, xl: 2, xxl: 2 }} items={[
          { key: 'business-status', label: '业务状态', children: statusTag(detail, 'business') },
          { key: 'payment-status', label: '支付状态', children: statusTag(detail, 'payment') },
          { key: 'start-source', label: '启动来源', children: startSourceTag(detail.start_source) },
          { key: 'user', label: '用户 ID', children: onUserSelect && canReadChargeUsers && isChargeUserID(detail.user_id)
            ? <Button type="link" style={{ padding: 0, height: 'auto' }} aria-label={`查看用户 ${detail.user_id} 档案`}
              onClick={() => onUserSelect?.(detail.user_id)}>{detail.user_id}</Button>
            : isChargeUserID(detail.user_id) ? detail.user_id : '—' },
          { key: 'station', label: '站点', children: stationLink(detail) },
          { key: 'device', label: '设备 / 端口', children: `${detail.device_id} / ${detail.port_no}` },
          { key: 'created', label: '创建时间', children: time(detail.created_at), span: 'filled' },
          { key: 'started', label: '开始时间', children: time(detail.started_at), span: 'filled' },
          { key: 'ended', label: '结束时间', children: time(detail.ended_at), span: 'filled' },
          { key: 'duration', label: '充电时间', children: orderDuration(detail) },
          { key: 'meter', label: '电量 (kWh)', children: orderMeter(detail) },
        ]} />
          },
          { key: 'package', label: '所选套餐', children: <OrderPackageDetails value={detail.selected_package} /> },
          { key: 'power', label: '功率曲线', children: <OrderPowerCurve key={detail.order_id} orderID={detail.order_id}
            active={detailTab === 'power'} charging={detail.business_status === 'charging'} /> },
          { key: 'payment', label: '费用与支付', children: <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        {detail.status === 'charging' && <Alert type="info" showIcon message="充电中的费用为按冻结费率计算的估算值，结束后以最终结算为准。" />}
        <Descriptions bordered column={{ xs: 1, sm: 2, md: 2, lg: 2, xl: 2, xxl: 2 }} items={[
          { key: 'electric', label: '电费', children: orderFee(detail, 'electric') },
          { key: 'service', label: '服务费', children: orderFee(detail, 'service') },
          { key: 'total', label: '总费用', children: orderFee(detail, 'total') },
          { key: 'paid', label: '实付', children: detail.paid_cents == null ? '—' : money(detail.paid_cents) },
          { key: 'payment', label: '支付单号', children: detail.payment_order_no || '—', span: 'filled' },
          { key: 'payment-status', label: '支付状态', children: statusTag(detail, 'payment') },
          { key: 'receipt', label: '计费单号', children: detail.billing?.calculation_no || '尚未生成' },
          { key: 'refund', label: '退款进度', children: refunds[detail.refund_status] || detail.refund_status },
          { key: 'refunded', label: '退款金额', children: refundedAmount(detail.refunded_cents) },
        ]} />
        {detail.refund_applicant_id && <ManualRefund key={detail.order_id} orderId={detail.order_id} orderNo={detail.order_no} actorId={detail.refund_applicant_id} onCreated={changed} />}
          </Space> },
          { key: 'billing', label: '分账明细', children: <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        {!detail.billing?.settlements.length && <Typography.Text type="secondary">暂无分账记录</Typography.Text>}
        {detail.billing?.settlements.map(settlement => <div key={settlement.settlement_id}>
          <Typography.Paragraph>{settlement.settlement_no} · {settlement.mode === 'mode_a' ? '全额分账' : '服务费分账'} · 分账池 {money(settlement.split_pool_cents)} · {settlement.status}</Typography.Paragraph>
          <Table rowKey="party_code" size="middle" pagination={TABLE_PAGINATION} dataSource={settlement.parties} columns={[
            { title: '参与方', key: 'party', render: (_, party) => party.party_name || party.party_code },
            { title: '比例', dataIndex: 'ratio_bp', render: value => `${(value / 100).toFixed(2)}%` },
            { title: '金额', dataIndex: 'amount_cents', render: money },
            { title: '状态', dataIndex: 'status', render: value => ({ pending: '待支付', paid: '已支付', failed: '失败' }[value as string] || value) },
          ]} />
        </div>)}
          </Space> },
          { key: 'timeline', label: '事件时间线', children: <>
        {!timeline?.timeline.length && <Typography.Text type="secondary">暂无已记录的事件</Typography.Text>}
        <Timeline items={timeline?.timeline.map(event => ({
          key: event.event_id,
          children: <><div>{time(event.at)} · {event.detail}</div><Typography.Text type="secondary">{event.actor} · {event.event}</Typography.Text></>,
        }))} />
          </> },
        ]} />
      </Space>}
      {children}
      <StationDetailsDrawer station={canReadStations ? orderStation : null}
        permissions={permissions} onClose={() => setOrderStation(null)} onSaved={stationSaved} />
  </>;
}
