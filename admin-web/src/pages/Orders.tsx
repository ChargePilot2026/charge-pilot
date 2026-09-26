import { Alert, Button, DatePicker, Descriptions, Drawer, Form, Input, InputNumber, Select, Space, Spin, Table, Tag, Timeline, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useEffect, useState } from 'react';
import dayjs, { Dayjs } from 'dayjs';
import axios from 'axios';
import { ApiEnvelope, http } from '../api/client';

interface Order {
  order_id: number;
  order_no: string;
  user_id: number;
  device_id: string;
  port_no: number;
  station_id: number | null;
  station_name: string | null;
  status: string;
  created_at: string;
  started_at: string | null;
  ended_at: string | null;
  duration_seconds: number | null;
  meter_kwh: string | null;
  electric_fee_cents: number | null;
  service_fee_cents: number | null;
  total_fee_cents: number | null;
  refund_status: string;
}
interface OrderPage { items: Order[]; total: number; page: number; page_size: number }
interface OrderTimeline { order_id: number; timeline: { event_id: string; at: string; event: string; actor: string; detail: string }[] }
interface OrderDetail extends Order {
  billing: { calculation_no: string | null; settlements: Settlement[] } | null;
  payment_order_id: number | null;
  payment_order_no: string | null;
  payment_status: string | null;
  paid_cents: number | null;
  refunded_cents: number | null;
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
  status?: string;
  period?: [Dayjs, Dayjs];
}
const statuses: Record<string, { label: string; color: string }> = {
  pending_payment: { label: '待支付', color: 'gold' }, paid: { label: '已支付', color: 'blue' },
  charging: { label: '充电中', color: 'green' }, completed: { label: '已完成', color: 'cyan' },
  cancelled: { label: '已取消', color: 'default' }, failed: { label: '失败', color: 'red' },
  refunding: { label: '退款中', color: 'orange' }, refunded: { label: '已退款', color: 'purple' },
};
const refunds: Record<string, string> = { none: '无退款', processing: '退款中', refunded: '已全额退款', partial_refunded: '部分退款' };
const time = (value: string | null) => value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '—';
const money = (value: number | null) => value == null ? '待结算' : `¥${(value / 100).toFixed(2)}`;
const statusTag = (value: string) => <Tag color={statuses[value]?.color}>{statuses[value]?.label || value}</Tag>;
function errorMessage(error: unknown): string {
  if (axios.isAxiosError<ApiEnvelope>(error)) {
    return error.response?.data?.message || '网络连接失败，请稍后重试';
  }
  return error instanceof Error ? error.message : '加载失败，请稍后重试';
}
function initialFilters(): Filters { return { period: [dayjs().subtract(7, 'day'), dayjs()] }; }

export default function OrdersPage() {
  const [form] = Form.useForm<Filters>();
  const [filters, setFilters] = useState<Filters>(initialFilters);
  const [pagination, setPagination] = useState({ page: 1, page_size: 20 });
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
    const controller = new AbortController();
    setLoading(true); setError(null); setPage(null);
    const { period, ...values } = filters;
    http.get<ApiEnvelope<OrderPage>>('/api/v1/admin/orders', {
      signal: controller.signal,
      params: {
        ...values, ...pagination,
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
    setDetail(null); setTimeline(null); setDetailError(null); setDetailLoading(true);
    Promise.all([
      http.get<ApiEnvelope<OrderDetail>>(`/api/v1/admin/orders/${selected}`, { signal: controller.signal }),
      http.get<ApiEnvelope<OrderTimeline>>(`/api/v1/admin/orders/${selected}/timeline`, { signal: controller.signal }),
    ]).then(([response, events]) => { if (!controller.signal.aborted) { setDetail(response.data.data); setTimeline(events.data.data); } })
      .catch(error => { if (!controller.signal.aborted) setDetailError(errorMessage(error)); })
      .finally(() => { if (!controller.signal.aborted) setDetailLoading(false); });
    return () => controller.abort();
  }, [selected, detailReload]);

  return <div className="page-container">
    <Space style={{ marginBottom: 16 }}>
      <Typography.Title level={3} style={{ margin: 0 }}>订单</Typography.Title>
      <Button icon={<ReloadOutlined />} onClick={() => setReload(value => value + 1)}>刷新</Button>
    </Space>
    <Form form={form} initialValues={filters} layout="inline" style={{ rowGap: 12, marginBottom: 20 }}
      onFinish={values => { setFilters(values); setPagination(value => ({ ...value, page: 1 })); }}>
      <Form.Item name="order_no" label="订单号"><Input allowClear maxLength={64} placeholder="完整订单号" /></Form.Item>
      <Form.Item name="device_id" label="设备"><Input allowClear maxLength={64} placeholder="设备编号" /></Form.Item>
      <Form.Item name="station_id" label="站点 ID"><InputNumber min={1} precision={0} placeholder="全部站点" /></Form.Item>
      <Form.Item name="status" label="状态"><Select allowClear placeholder="全部状态" style={{ width: 130 }}
        options={Object.entries(statuses).map(([value, status]) => ({ value, label: status.label }))} /></Form.Item>
      <Form.Item name="period" label="时间范围"><DatePicker.RangePicker showTime format="YYYY-MM-DD HH:mm" /></Form.Item>
      <Form.Item><Space>
        <Button type="primary" htmlType="submit">查询</Button>
        <Button onClick={() => { const values = initialFilters(); form.resetFields(); form.setFieldsValue(values); setFilters(values); setPagination(value => ({ ...value, page: 1 })); }}>重置</Button>
      </Space></Form.Item>
    </Form>
    <Typography.Paragraph type="secondary">默认查询近 7 天，时间按本地时区显示。尚未启动的订单按创建时间筛选。</Typography.Paragraph>
    {error && <Alert type="error" showIcon message="订单加载失败" description={error} style={{ marginBottom: 16 }}
      action={<Button onClick={() => setReload(value => value + 1)}>重试</Button>} />}
    <Table<Order> rowKey="order_id" loading={loading} dataSource={page?.items || []} scroll={{ x: 1500 }}
      locale={{ emptyText: error ? '暂时无法获取订单' : '当前条件下没有订单' }}
      pagination={{ current: pagination.page, pageSize: pagination.page_size, total: page?.total || 0,
        showSizeChanger: true, pageSizeOptions: [20, 50, 100], showTotal: total => `共 ${total} 笔`,
        onChange: (page, page_size) => setPagination({ page: page_size !== pagination.page_size ? 1 : page, page_size }) }}
      columns={[
        { title: '订单号', dataIndex: 'order_no', width: 230, fixed: 'left', render: (value, row) => <Button type="link" style={{ padding: 0 }} onClick={() => setSelected(row.order_id)}>{value}</Button> },
        { title: '站点 / 设备', key: 'device', width: 180, render: (_, row) => <><div>{row.station_name || '未关联站点'}</div><Typography.Text type="secondary">{row.device_id} · 端口 {row.port_no}</Typography.Text></> },
        { title: '状态', dataIndex: 'status', width: 110, render: statusTag },
        { title: '电量 (kWh)', dataIndex: 'meter_kwh', width: 110, render: value => value ?? '—' },
        { title: '电费', dataIndex: 'electric_fee_cents', width: 100, render: money },
        { title: '服务费', dataIndex: 'service_fee_cents', width: 100, render: money },
        { title: '总费用', dataIndex: 'total_fee_cents', width: 110, render: money },
        { title: '退款', dataIndex: 'refund_status', width: 120, render: value => refunds[value] || value },
        { title: '开始时间', dataIndex: 'started_at', width: 180, render: time },
        { title: '结束时间', dataIndex: 'ended_at', width: 180, render: time },
      ]} />
    <Drawer title="订单详情" open={selected != null} onClose={() => setSelected(null)} width="min(760px, 100vw)">
      {detailLoading && <Spin />}
      {detailError && <Alert type="error" showIcon message="详情加载失败" description={detailError}
        action={<Button onClick={() => setDetailReload(value => value + 1)}>重试</Button>} />}
      {detail && <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <Descriptions title={detail.order_no} bordered column={2} items={[
          { key: 'status', label: '状态', children: statusTag(detail.status) },
          { key: 'user', label: '用户 ID', children: detail.user_id },
          { key: 'station', label: '站点', children: detail.station_name || '未关联站点' },
          { key: 'device', label: '设备 / 端口', children: `${detail.device_id} / ${detail.port_no}` },
          { key: 'created', label: '创建时间', children: time(detail.created_at), span: 2 },
          { key: 'started', label: '开始时间', children: time(detail.started_at), span: 2 },
          { key: 'ended', label: '结束时间', children: time(detail.ended_at), span: 2 },
          { key: 'duration', label: '时长', children: detail.duration_seconds == null ? '—' : `${detail.duration_seconds} 秒` },
          { key: 'meter', label: '电量', children: detail.meter_kwh == null ? '—' : `${detail.meter_kwh} kWh` },
        ]} />
        <Descriptions title="费用与支付" bordered column={2} items={[
          { key: 'electric', label: '电费', children: money(detail.electric_fee_cents) },
          { key: 'service', label: '服务费', children: money(detail.service_fee_cents) },
          { key: 'total', label: '总费用', children: money(detail.total_fee_cents) },
          { key: 'paid', label: '实付', children: detail.paid_cents == null ? '—' : money(detail.paid_cents) },
          { key: 'payment', label: '支付单号', children: detail.payment_order_no || '—', span: 2 },
          { key: 'refund', label: '退款状态', children: refunds[detail.refund_status] || detail.refund_status },
          { key: 'refunded', label: '已退金额', children: detail.refunded_cents == null ? '—' : money(detail.refunded_cents) },
        ]} />
        {detail.failure_reason && <Alert type="warning" message="异常原因" description={detail.failure_reason} showIcon />}
        <Typography.Title level={5}>分账明细</Typography.Title>
        {!detail.billing?.settlements.length && <Typography.Text type="secondary">暂无分账记录</Typography.Text>}
        {detail.billing?.settlements.map(settlement => <div key={settlement.settlement_id}>
          <Typography.Paragraph>{settlement.settlement_no} · {settlement.mode === 'mode_a' ? '全额分账' : '服务费分账'} · 分账池 {money(settlement.split_pool_cents)} · {settlement.status}</Typography.Paragraph>
          <Table rowKey="party_code" size="small" pagination={false} dataSource={settlement.parties} columns={[
            { title: '参与方', key: 'party', render: (_, party) => party.party_name || party.party_code },
            { title: '比例', dataIndex: 'ratio_bp', render: value => `${(value / 100).toFixed(2)}%` },
            { title: '金额', dataIndex: 'amount_cents', render: money },
            { title: '状态', dataIndex: 'status', render: value => ({ pending: '待支付', paid: '已支付', failed: '失败' }[value as string] || value) },
          ]} />
        </div>)}
        <Typography.Title level={5}>事件时间线</Typography.Title>
        {!timeline?.timeline.length && <Typography.Text type="secondary">暂无已记录的事件</Typography.Text>}
        <Timeline items={timeline?.timeline.map(event => ({
          key: event.event_id,
          children: <><div>{time(event.at)} · {event.detail}</div><Typography.Text type="secondary">{event.actor} · {event.event}</Typography.Text></>,
        }))} />
      </Space>}
    </Drawer>
  </div>;
}
