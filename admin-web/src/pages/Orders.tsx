import { Alert, Button, DatePicker, Descriptions, Drawer, Form, Input, Select, Space, Spin, Table, Tabs, Tag, Timeline, Tooltip, Typography } from 'antd';
import { CreditCardOutlined, DownOutlined, QrcodeOutlined, UpOutlined, WalletOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import dayjs, { Dayjs } from 'dayjs';
import axios from 'axios';
import { ApiEnvelope, http } from '../api/client';
import { LoadError } from '../components/LoadError';
import ManualRefund from './ManualRefund';
import OrderPackageDetails, { type SelectedPackage } from './orders/OrderPackageDetails';
import OrderStationSelect from './orders/OrderStationSelect';
import { businessStatuses, businessStatusInfo, paymentStatuses, paymentStatusInfo, chargingDuration, refundedAmount } from './orders/presentation';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../utils/tablePagination';

interface Order {
  live?: { at: string; stale: boolean; kwh: number; seconds: number; fee?: { electric_cents: number; service_cents: number; total_cents: number }; fee_unavailable?: string };
  live_unavailable?: string;
  order_id: number;
  order_no: string;
  user_id: number;
  device_id: string;
  port_no: number;
  station_id: number | null;
  station_name: string | null;
  status: string;
  business_status: string;
  payment_status: string;
  payment_order_status?: string | null;
  start_source: 'payment' | 'balance' | 'card' | null;
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
interface OrderPage { items: Order[]; total: number; page: number; page_size: number }
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
interface Filters {
  order_no?: string;
  device_id?: string;
  station_id?: number;
  business_status?: string;
  payment_status?: string;
  start_source?: string;
  period?: [Dayjs, Dayjs];
}
const refunds: Record<string, string> = { none: '无退款', processing: '退款中', refunded: '已全额退款', partial_refunded: '部分退款' };
const businessStatusColors: Record<string, string> = { pending_start: '#ffa940', charging: '#4096ff', completed: 'default' };
const paymentStatusColors: Record<string, string> = { pending: '#ffa940', paid: '#73d13d', refunded: '#ff4d4f', partial_refunded: '#ff7a45' };
const time = (value: string | null) => value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '—';
const money = (value: number | null) => value == null ? '待结算' : `¥${(value / 100).toFixed(2)}`;
const statusTag = (row: Order, kind: 'business' | 'payment') => {
  const info = kind === 'business' ? businessStatusInfo(row) : paymentStatusInfo(row);
  const color = kind === 'business' ? businessStatusColors[row.business_status] : paymentStatusColors[row.payment_status];
  const tag = <Tag color={color || info.color}>{info.label}</Tag>;
  return info.hint ? <Tooltip title={info.hint}>{tag}</Tooltip> : tag;
};
const orderDuration = (row: Order) => {
  const duration = chargingDuration(row);
  const hint = `${duration.hint || ''}${duration.sampledAt ? `采样时间：${time(duration.sampledAt)}。` : ''}`;
  return hint ? <Tooltip title={hint}><span>{duration.text}</span></Tooltip> : duration.text;
};
const liveHint = (row: Order) => row.live ? `${row.live.stale ? '读数已过期，保留最后采样值；' : ''}采样时间：${time(row.live.at)}。费用按订单冻结费率估算，结束后结算。` : row.live_unavailable || '等待设备计量';
const orderMeter = (row: Order) => row.status === 'charging'
  ? <Tooltip title={liveHint(row)}><span>{row.live ? `${row.live.kwh.toFixed(3)}${row.live.stale ? '（旧）' : ''}` : '待上报'}</span></Tooltip>
  : row.meter_kwh ?? '—';
const orderFee = (row: Order, kind: 'electric' | 'service' | 'total') => row.status === 'charging'
  ? <Tooltip title={`${liveHint(row)} ${row.live?.fee_unavailable || ''}`}><span>{row.live?.fee ? `≈${money(row.live.fee[`${kind}_cents`])}${row.live.stale ? '（旧）' : ''}` : '待计量'}</span></Tooltip>
  : money(row[`${kind}_fee_cents`]);
function errorMessage(error: unknown): string {
  if (axios.isAxiosError<ApiEnvelope>(error)) {
    return error.response?.data?.message || '网络连接失败，请稍后重试';
  }
  return error instanceof Error ? error.message : '加载失败，请稍后重试';
}
const startSources: Record<string, string> = { payment: '扫码支付', balance: '余额支付', card: '在线卡' };
const startSourceIcons = { payment: <QrcodeOutlined />, balance: <WalletOutlined />, card: <CreditCardOutlined /> };
const startSourceTag = (source: Order['start_source']) => source && startSources[source]
  ? <Tag icon={startSourceIcons[source]}>{startSources[source]}</Tag> : '—';
function initialFilters(orderNo?: string): Filters { return { station_id: 0, order_no: orderNo }; }

export default function OrdersPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const linkedOrderNo = searchParams.get('order_no')?.trim() || undefined;
  const previousLinkedOrderNo = useRef(linkedOrderNo);
  const [form] = Form.useForm<Filters>();
  const [filtersExpanded, setFiltersExpanded] = useState(false);
  const stationFilter = Form.useWatch('station_id', form);
  const businessStatusFilter = Form.useWatch('business_status', form);
  const paymentStatusFilter = Form.useWatch('payment_status', form);
  const startSourceFilter = Form.useWatch('start_source', form);
  const advancedFilterCount = Number(Boolean(stationFilter)) + Number(Boolean(businessStatusFilter))
    + Number(Boolean(paymentStatusFilter)) + Number(Boolean(startSourceFilter));
  const [filters, setFilters] = useState<Filters>(() => initialFilters(linkedOrderNo));
  const [pagination, setPagination] = useState({ page: 1, page_size: DEFAULT_PAGE_SIZE });
  const [reload, setReload] = useState(0);
  const [page, setPage] = useState<OrderPage | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [selected, setSelected] = useState<number | null>(null);
  const [detail, setDetail] = useState<OrderDetail | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  const [detailReload, setDetailReload] = useState(0);
  const [timeline, setTimeline] = useState<OrderTimeline | null>(null);

  useEffect(() => {
    if (previousLinkedOrderNo.current === linkedOrderNo) return;
    previousLinkedOrderNo.current = linkedOrderNo;
    const values = initialFilters(linkedOrderNo);
    form.resetFields(); form.setFieldsValue(values);
    setFilters(values); setPagination(value => ({ ...value, page: 1 })); setSelected(null);
  }, [linkedOrderNo, form]);

  useEffect(() => {
    if (!page?.items.some(row => row.status === 'charging')) return;
    const timer = window.setInterval(() => setReload(value => value + 1), 5000);
    return () => window.clearInterval(timer);
  }, [page?.items]);
  useEffect(() => {
    if (detail?.status !== 'charging') return;
    const timer = window.setInterval(() => setDetailReload(value => value + 1), 5000);
    return () => window.clearInterval(timer);
  }, [detail?.status, selected]);

  useEffect(() => {
    const controller = new AbortController();
    if (!page) setLoading(true); setError(null);
    const { period, ...values } = filters;
    http.get<ApiEnvelope<OrderPage>>('/api/v1/admin/orders', {
      signal: controller.signal,
      params: {
        ...values, ...pagination,
        station_id: values.station_id || undefined,
        order_no: values.order_no?.trim() || undefined,
        device_id: values.device_id?.trim() || undefined,
        started_from: period?.[0]?.toISOString(), started_to: period?.[1]?.toISOString(),
      },
    }).then(response => { if (!controller.signal.aborted) setPage(response.data.data); })
      .catch(error => { if (!controller.signal.aborted) setError(errorMessage(error)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [filters, pagination, reload]);

  useEffect(() => {
    if (selected == null) return;
    const controller = new AbortController();
    if (detail?.order_id !== selected) { setDetail(null); setTimeline(null); setDetailLoading(true); }
    setDetailError(null);
    Promise.all([
      http.get<ApiEnvelope<OrderDetail>>(`/api/v1/admin/orders/${selected}`, { signal: controller.signal }),
      http.get<ApiEnvelope<OrderTimeline>>(`/api/v1/admin/orders/${selected}/timeline`, { signal: controller.signal }),
    ]).then(([response, events]) => { if (!controller.signal.aborted) { setDetail(response.data.data); setTimeline(events.data.data); } })
      .catch(error => { if (!controller.signal.aborted) setDetailError(errorMessage(error)); })
      .finally(() => { if (!controller.signal.aborted) setDetailLoading(false); });
    return () => controller.abort();
  }, [selected, detailReload]);

  return <div className="page-container">
    <Form form={form} initialValues={initialFilters(linkedOrderNo)} layout="inline" style={{ display: 'block', marginBottom: 20 }}
      onFinish={values => { setFilters(values); setPagination(value => ({ ...value, page: 1 })); }}>
      <div style={{ display: 'flex', flexWrap: 'wrap', rowGap: 12 }}>
        <Form.Item name="order_no" label="订单号"><Input allowClear maxLength={64} placeholder="完整订单号" /></Form.Item>
        <Form.Item name="device_id" label="设备编号"><Input allowClear maxLength={64} placeholder="设备编号" /></Form.Item>
        <Form.Item name="period" label="时间范围"><DatePicker.RangePicker showTime format="YYYY-MM-DD HH:mm" /></Form.Item>
        <Form.Item><Space>
          <Button type="primary" htmlType="submit">查询</Button>
          <Button onClick={() => { const values = initialFilters(); form.resetFields(); form.setFieldsValue(values); setFilters(values); setFiltersExpanded(false); setPagination(value => ({ ...value, page: 1 })); if (linkedOrderNo) { const params = new URLSearchParams(searchParams); params.delete('order_no'); setSearchParams(params, { replace: true }); } }}>重置</Button>
          <Button type="link" icon={filtersExpanded ? <UpOutlined /> : <DownOutlined />} aria-expanded={filtersExpanded}
            aria-controls="order-advanced-filters" onClick={() => setFiltersExpanded(value => !value)}>
            {filtersExpanded ? '收起条件' : '更多条件'}{advancedFilterCount > 0 && `（${advancedFilterCount}）`}
          </Button>
        </Space></Form.Item>
      </div>
      <div id="order-advanced-filters" style={{ display: filtersExpanded ? 'flex' : 'none', flexWrap: 'wrap', rowGap: 12, marginTop: 12 }}>
        <Form.Item name="station_id" label="站点"><OrderStationSelect /></Form.Item>
        <Form.Item name="business_status" label="业务状态"><Select allowClear placeholder="全部业务状态" style={{ width: 150 }}
          options={Object.entries(businessStatuses).map(([value, status]) => ({ value, label: status.label }))} /></Form.Item>
        <Form.Item name="payment_status" label="支付状态"><Select allowClear placeholder="全部支付状态" style={{ width: 150 }}
          options={Object.entries(paymentStatuses).map(([value, status]) => ({ value, label: status.label }))} /></Form.Item>
        <Form.Item name="start_source" label="启动来源"><Select allowClear placeholder="全部来源" style={{ width: 150 }}
          options={Object.entries(startSources).map(([value, label]) => ({ value, label }))} /></Form.Item>
      </div>
    </Form>
    {error && <LoadError title="订单加载失败" detail={error} onRetry={() => setReload(value => value + 1)} />}
    <Table<Order> size="middle" rowKey="order_id" loading={loading} dataSource={page?.items || []} tableLayout="auto" scroll={{ x: 'max-content' }}
      locale={{ emptyText: error ? '暂时无法获取订单' : '当前条件下没有订单' }}
      pagination={{ ...TABLE_PAGINATION, current: pagination.page, pageSize: pagination.page_size, total: page?.total || 0,
        position: ['topRight', 'bottomRight'],
        showTotal: total => `共 ${total} 笔`,
        onChange: (page, page_size) => setPagination({ page: page_size !== pagination.page_size ? 1 : page, page_size }) }}
      columns={[
        { title: '订单号', dataIndex: 'order_no', fixed: 'left', onCell: () => ({ style: { whiteSpace: 'nowrap' } }), render: (value, row) => <Button type="link" style={{ padding: 0 }} onClick={() => setSelected(row.order_id)}>{value}</Button> },
        { title: '站点', dataIndex: 'station_name', width: 180, render: value => value || '未关联站点' },
        { title: '设备', dataIndex: 'device_id', onCell: () => ({ style: { whiteSpace: 'nowrap' } }) },
        { title: '端口', dataIndex: 'port_no', width: 80 },
        { title: '启动来源', dataIndex: 'start_source', width: 130, render: startSourceTag },
        { title: '业务状态', width: 110, render: (_, row) => statusTag(row, 'business') },
        { title: '支付状态', width: 130, render: (_, row) => statusTag(row, 'payment') },
        { title: '充电时间', width: 160, render: (_, row) => orderDuration(row) },
        { title: '电量 (kWh)', width: 130, render: (_, row) => orderMeter(row) },
        { title: '电费', width: 120, render: (_, row) => orderFee(row, 'electric') },
        { title: '服务费', width: 120, render: (_, row) => orderFee(row, 'service') },
        { title: '总费用', width: 120, render: (_, row) => orderFee(row, 'total') },
        { title: '退款金额', dataIndex: 'refunded_cents', width: 120, render: refundedAmount },
        { title: '开始时间', dataIndex: 'started_at', width: 180, render: time },
        { title: '结束时间', dataIndex: 'ended_at', width: 180, render: time },
      ]} />
    <Drawer title="订单详情" open={selected != null} onClose={() => setSelected(null)} width="min(1100px, 100vw)">
      {detailLoading && <Spin />}
      {detailError && <LoadError title="详情加载失败" detail={detailError} onRetry={() => setDetailReload(value => value + 1)} />}
      {detail && <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <Space wrap><Typography.Text strong copyable>{detail.order_no}</Typography.Text>{statusTag(detail, 'business')}{statusTag(detail, 'payment')}</Space>
        {detail.failure_reason && <Alert type="warning" message="异常原因" description={detail.failure_reason} showIcon />}
        <Tabs key={detail.order_id} style={{ width: '100%' }} items={[
          { key: 'basic', label: '基本信息', children: <Descriptions bordered column={{ xs: 1, sm: 2, md: 2, lg: 2, xl: 2, xxl: 2 }} items={[
          { key: 'business-status', label: '业务状态', children: statusTag(detail, 'business') },
          { key: 'payment-status', label: '支付状态', children: statusTag(detail, 'payment') },
          { key: 'start-source', label: '启动来源', children: startSourceTag(detail.start_source) },
          { key: 'user', label: '用户 ID', children: detail.user_id },
          { key: 'station', label: '站点', children: detail.station_name || '未关联站点' },
          { key: 'device', label: '设备 / 端口', children: `${detail.device_id} / ${detail.port_no}` },
          { key: 'created', label: '创建时间', children: time(detail.created_at), span: 2 },
          { key: 'started', label: '开始时间', children: time(detail.started_at), span: 2 },
          { key: 'ended', label: '结束时间', children: time(detail.ended_at), span: 2 },
          { key: 'duration', label: '充电时间', children: orderDuration(detail) },
          { key: 'meter', label: '电量 (kWh)', children: orderMeter(detail) },
        ]} />
          },
          { key: 'package', label: '所选套餐', children: <OrderPackageDetails value={detail.selected_package} /> },
          { key: 'payment', label: '费用与支付', children: <Space direction="vertical" size="middle" style={{ width: '100%' }}>
        {detail.status === 'charging' && <Alert type="info" showIcon message="充电中的费用为按冻结费率计算的估算值，结束后以最终结算为准。" />}
        <Descriptions bordered column={{ xs: 1, sm: 2, md: 2, lg: 2, xl: 2, xxl: 2 }} items={[
          { key: 'electric', label: '电费', children: orderFee(detail, 'electric') },
          { key: 'service', label: '服务费', children: orderFee(detail, 'service') },
          { key: 'total', label: '总费用', children: orderFee(detail, 'total') },
          { key: 'paid', label: '实付', children: detail.paid_cents == null ? '—' : money(detail.paid_cents) },
          { key: 'payment', label: '支付单号', children: detail.payment_order_no || '—', span: 2 },
          { key: 'payment-status', label: '支付状态', children: statusTag(detail, 'payment') },
          { key: 'receipt', label: '计费单号', children: detail.billing?.calculation_no || '尚未生成' },
          { key: 'refund', label: '退款进度', children: refunds[detail.refund_status] || detail.refund_status },
          { key: 'refunded', label: '退款金额', children: refundedAmount(detail.refunded_cents) },
        ]} />
        {detail.refund_applicant_id && <ManualRefund key={detail.order_id} orderId={detail.order_id} orderNo={detail.order_no} actorId={detail.refund_applicant_id} onCreated={() => { setReload(value => value + 1); setDetailReload(value => value + 1); }} />}
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
    </Drawer>
  </div>;
}
