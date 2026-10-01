import { Button, Form, Input, Select, Space, Table, Tag, Tooltip } from 'antd';
import axios from 'axios';
import dayjs from 'dayjs';
import { useEffect, useState } from 'react';
import { Link } from 'react-router-dom';
import { type ApiEnvelope, http } from '../api/client';
import { LoadError } from '../components/LoadError';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../utils/tablePagination';
import { paymentStatuses, paymentStatusInfo, paymentStatusColors, refundedAmount } from './orders/presentation';

interface PaymentOrder {
  payment_order_id: number;
  order_no: string;
  user_id: number;
  biz_type: string;
  pay_method: string;
  status: string;
  payment_status: string;
  total_cents: number;
  paid_cents: number | null;
  refunded_cents: number | null;
  paid_at: string | null;
  created_at: string;
  charge_order_id: number | null;
  charge_order_no: string | null;
}
interface PaymentOrderPage { items: PaymentOrder[]; total: number; page: number; page_size: number }
interface Filters { order_no?: string; biz_type?: string; payment_status?: string; pay_method?: string }
const businessTypes: Record<string, string> = { charge: '充电支付', wallet_recharge: '余额充值' };
const paymentMethods: Record<string, string> = { wechat: '微信支付', balance: '余额支付' };
const businessTypeColors: Record<string, string> = { charge: 'blue', wallet_recharge: 'purple' };
const paymentMethodColors: Record<string, string> = { wechat: 'green', balance: 'gold' };
const presetTextColors: Record<string, string> = {
  blue: '#0958d9', purple: '#531dab', green: '#389e0d', gold: '#d48806', orange: '#d46b08',
};
const tagTextStyle = (color?: string) => color && presetTextColors[color] ? { color: presetTextColors[color] } : undefined;
const time = (value: string | null) => value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '—';
const paymentTag = (row: PaymentOrder) => {
  const info = paymentStatusInfo({ payment_status: row.payment_status, payment_order_status: row.status });
  const color = paymentStatusColors[row.payment_status];
  const tag = <Tag style={color ? { color } : undefined}>{info.label}</Tag>;
  return info.hint ? <Tooltip title={info.hint}>{tag}</Tooltip> : tag;
};
const errorMessage = (cause: unknown) => axios.isAxiosError<ApiEnvelope>(cause)
  ? cause.response?.data?.message || cause.message
  : cause instanceof Error ? cause.message : '支付订单读取失败，请稍后重试';

export default function PaymentOrdersPage() {
  const [form] = Form.useForm<Filters>();
  const [filters, setFilters] = useState<Filters>({});
  const [pagination, setPagination] = useState({ page: 1, page_size: DEFAULT_PAGE_SIZE });
  const [page, setPage] = useState<PaymentOrderPage | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [reload, setReload] = useState(0);

  useEffect(() => {
    const controller = new AbortController();
    setLoading(true); setError('');
    http.get<ApiEnvelope<PaymentOrderPage>>('/api/v1/admin/payment-orders', {
      signal: controller.signal,
      params: { ...filters, ...pagination, order_no: filters.order_no?.trim() || undefined },
    }).then(response => { if (!controller.signal.aborted) setPage(response.data.data); })
      .catch(cause => { if (!controller.signal.aborted) setError(errorMessage(cause)); })
      .finally(() => { if (!controller.signal.aborted) setLoading(false); });
    return () => controller.abort();
  }, [filters, pagination, reload]);

  return <div className="page-container">
    <Form form={form} layout="inline" style={{ display: 'flex', flexWrap: 'wrap', rowGap: 12, marginBottom: 20 }}
      onFinish={values => { setFilters(values); setPagination(current => ({ ...current, page: 1 })); }}>
      <Form.Item name="order_no" label="支付单号"><Input allowClear maxLength={64} placeholder="完整支付单号" /></Form.Item>
      <Form.Item name="biz_type" label="用途"><Select allowClear placeholder="全部用途" style={{ width: 130 }}
        options={Object.entries(businessTypes).map(([value, label]) => ({ value, label }))} /></Form.Item>
      <Form.Item name="pay_method" label="支付方式"><Select allowClear placeholder="全部方式" style={{ width: 130 }}
        options={Object.entries(paymentMethods).map(([value, label]) => ({ value, label }))} /></Form.Item>
      <Form.Item name="payment_status" label="支付状态"><Select allowClear placeholder="全部状态" style={{ width: 150 }}
        options={Object.entries(paymentStatuses).map(([value, status]) => ({ value, label: status.label }))} /></Form.Item>
      <Form.Item><Space>
        <Button type="primary" htmlType="submit">查询</Button>
        <Button onClick={() => { form.resetFields(); setFilters({}); setPagination(current => ({ ...current, page: 1 })); }}>重置</Button>
      </Space></Form.Item>
    </Form>
    {error && <LoadError title="支付订单加载失败" detail={error} onRetry={() => setReload(value => value + 1)} />}
    <Table<PaymentOrder> size="middle" rowKey="payment_order_id" loading={loading} dataSource={page?.items || []}
      tableLayout="auto" scroll={{ x: 'max-content' }} locale={{ emptyText: error ? '暂时无法获取支付订单' : '当前条件下没有支付订单' }}
      pagination={{ ...TABLE_PAGINATION, current: pagination.page, pageSize: pagination.page_size, total: page?.total || 0,
        showTotal: total => `共 ${total} 笔`,
        onChange: (page, page_size) => setPagination({ page: page_size !== pagination.page_size ? 1 : page, page_size }) }}
      columns={[
        { title: '支付单号', dataIndex: 'order_no', fixed: 'left', onCell: () => ({ style: { whiteSpace: 'nowrap' } }) },
        { title: '用途', dataIndex: 'biz_type', width: 110, render: value => value
          ? <Tag style={tagTextStyle(businessTypeColors[value])}>{businessTypes[value] || value}</Tag> : '—' },
        { title: '支付方式', dataIndex: 'pay_method', width: 110, render: value => value
          ? <Tag style={tagTextStyle(paymentMethodColors[value])}>{paymentMethods[value] || value}</Tag> : '—' },
        { title: '支付状态', width: 130, render: (_, row) => paymentTag(row) },
        { title: '应付金额', dataIndex: 'total_cents', width: 120, render: refundedAmount },
        { title: '实付金额', dataIndex: 'paid_cents', width: 120, render: refundedAmount },
        { title: '退款金额', dataIndex: 'refunded_cents', width: 120, render: refundedAmount },
        { title: '支付时间', dataIndex: 'paid_at', width: 180, render: time },
        { title: '创建时间', dataIndex: 'created_at', width: 180, render: time },
        { title: '关联充电订单', key: 'charge_order', onCell: () => ({ style: { whiteSpace: 'nowrap' } }), render: (_, row) => row.charge_order_no
          ? <Link to={`/orders?${new URLSearchParams({ order_no: row.charge_order_no }).toString()}`}>{row.charge_order_no}</Link> : '—' },
      ]} />
  </div>;
}
