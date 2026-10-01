import { Button, DatePicker, Form, Input, Select, Space, Table } from 'antd';
import { DownOutlined, UpOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import type { Dayjs } from 'dayjs';
import { type ApiEnvelope, http } from '../api/client';
import { LoadError } from '../components/LoadError';
import OrderStationSelect from './orders/OrderStationSelect';
import ChargeUserProfileDrawer from './chargeUsers/ChargeUserProfileDrawer';
import StationDetailsDrawer, { type StationReference } from './stations/StationDetailsDrawer';
import type { StationRecord } from './stations/StationWorkspace';
import { businessStatuses, paymentStatuses, refundedAmount } from './orders/presentation';
import OrderDetailsDrawer, { type Order, cachedPermissions, time, statusTag, orderDuration, orderMeter, orderFee, errorMessage, startSources, startSourceTag } from './orders/OrderDetailsDrawer';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../utils/tablePagination';

interface OrderPage { items: Order[]; total: number; page: number; page_size: number }
interface Filters {
  order_no?: string;
  device_id?: string;
  station_id?: number;
  business_status?: string;
  payment_status?: string;
  start_source?: string;
  period?: [Dayjs, Dayjs];
}
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
  const [profileUser, setProfileUser] = useState<{ orderID: number; userID: string } | null>(null);
  const [permissions, setPermissions] = useState(cachedPermissions);
  const canReadChargeUsers = permissions.includes('charge_user.read');
  const canReadStations = permissions.includes('station.read');
  const [listStation, setListStation] = useState<StationReference | null>(null);

  useEffect(() => { setProfileUser(null); }, [selected]);
  useEffect(() => {
    const updatePermissions = () => setPermissions(cachedPermissions());
    window.addEventListener('cp-session', updatePermissions);
    window.addEventListener('storage', updatePermissions);
    return () => {
      window.removeEventListener('cp-session', updatePermissions);
      window.removeEventListener('storage', updatePermissions);
    };
  }, []);

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

  const stationLink = (row: Order) => {
    const name = row.station_name || (row.station_id ? `站点 #${row.station_id}` : '未关联站点');
    if (!canReadStations || !row.station_id || !Number.isSafeInteger(row.station_id) || row.station_id <= 0) return name;
    const station = { id: row.station_id, name: row.station_name };
    return <Button type="link" style={{ padding: 0, height: 'auto' }} aria-label={`查看${name}详情`}
      onClick={() => setListStation(station)}>{name}</Button>;
  };
  const stationSaved = (updated: StationRecord) => {
    setListStation(current => current?.id === updated.id ? updated : current);
    setReload(value => value + 1);
  };

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
        { title: '站点', dataIndex: 'station_name', width: 180, render: (_, row) => stationLink(row) },
        { title: '设备', dataIndex: 'device_id', onCell: () => ({ style: { whiteSpace: 'nowrap' } }) },
        { title: '端口', dataIndex: 'port_no', width: 80 },
        { title: '启动来源', dataIndex: 'start_source', width: 130, render: startSourceTag },
        { title: '所选套餐', key: 'selected_package', onCell: () => ({ style: { whiteSpace: 'nowrap' } }), render: (_, row) => row.selected_scheme_name && row.selected_package_name
          ? `${row.selected_scheme_name}（${row.selected_package_name}）` : row.selected_scheme_name || row.selected_package_name || '—' },
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
    <OrderDetailsDrawer orderID={selected} onClose={() => setSelected(null)} onChanged={() => setReload(value => value + 1)}
      onUserSelect={userID => { if (selected != null) setProfileUser({ orderID: selected, userID }); }}>
      <ChargeUserProfileDrawer userID={canReadChargeUsers && profileUser?.orderID === selected ? profileUser.userID : null}
        onClose={() => setProfileUser(null)} />
    </OrderDetailsDrawer>
    <StationDetailsDrawer station={canReadStations ? listStation : null} permissions={permissions}
      onClose={() => setListStation(null)} onSaved={stationSaved} />
  </div>;
}
