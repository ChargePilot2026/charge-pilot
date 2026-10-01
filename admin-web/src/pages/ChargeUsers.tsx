import { Alert, Avatar, Button, Descriptions, Drawer, Form, Input, Select, Space, Spin, Table, Tag, Typography } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';
import dayjs from 'dayjs';
import axios from 'axios';
import { ApiEnvelope, apiGet } from '../api/client';
import { LoadError } from '../components/LoadError';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../utils/tablePagination';

interface ChargeUser {
  id: number;
  openid: string;
  union_id: string | null;
  nickname: string | null;
  avatar_url: string | null;
  gender: string;
  status: string;
  first_seen_at: string;
  last_login_at: string | null;
  inviter_id: number | null;
  created_at: string;
  phone: string;
  phone_bound: boolean;
  balance_cents: number;
  frozen_cents: number;
  order_count: number;
  total_cents: number;
  last_order_at: string | null;
}
interface ChargeUserPage { items: ChargeUser[]; total: number; page: number; page_size: number }
interface ChargeUserOrder {
  order_id: number; order_no: string; device_id: string; station_id: number | null;
  status: string; total_cents: number | null; created_at: string; started_at: string | null;
}
interface ChargeUserDetail extends ChargeUser {
  wallet_status: string;
  recent_orders: ChargeUserOrder[];
  coupon_granted: number;
  coupon_unused: number;
  fault_reports: number;
}

const statuses: Record<string, { label: string; color: string }> = {
  active: { label: '正常', color: 'green' }, frozen: { label: '已冻结', color: 'red' },
};
const walletStatuses: Record<string, string> = { active: '正常', frozen: '已冻结' };
const genders: Record<string, string> = { unknown: '未知', male: '男', female: '女' };
const orderStatuses: Record<string, string> = {
  pending_payment: '待支付', paid: '已支付', charging: '充电中', completed: '已完成',
  cancelled: '已取消', failed: '失败', refunding: '退款中', refunded: '已退款',
};
const time = (value: string | null) => value ? dayjs(value).format('YYYY-MM-DD HH:mm') : '—';
const money = (value: number) => `¥${(value / 100).toFixed(2)}`;
const statusTag = (value: string) => <Tag color={statuses[value]?.color}>{statuses[value]?.label || value}</Tag>;
const name = (chargeUser: Pick<ChargeUser, 'nickname' | 'id'>) => chargeUser.nickname || `用户 #${chargeUser.id}`;

/** 手机号是这页最敏感的一列，复制和展示都按完整号码给，不再做打码。 */
function Phone({ chargeUser }: { chargeUser: Pick<ChargeUser, 'phone' | 'phone_bound' | 'id'> }) {
  if (!chargeUser.phone_bound) return <Typography.Text type="secondary">未绑定</Typography.Text>;
  return <Typography.Text copyable={{ text: chargeUser.phone }} style={{ fontVariantNumeric: 'tabular-nums' }}>{chargeUser.phone}</Typography.Text>;
}

export default function ChargeUsersPage() {
  const [form] = Form.useForm();
  const [filters, setFilters] = useState({ keyword: '', status: '' });
  const [query, setQuery] = useState({ page: 1, page_size: DEFAULT_PAGE_SIZE, keyword: '', status: '' });
  const [page, setPage] = useState<ChargeUserPage | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  const [selected, setSelected] = useState<number | null>(null);
  const [detail, setDetail] = useState<ChargeUserDetail | null>(null);
  const [detailError, setDetailError] = useState<string | null>(null);
  const [detailLoading, setDetailLoading] = useState(false);
  // 档案的重试：selected 固定时上面的 effect 不会再跑，光置同一个值等于没点。
  const [detailReload, setDetailReload] = useState(0);
  const generation = useRef(0);

  useEffect(() => {
    const current = ++generation.current;
    setLoading(true); setError(null);
    apiGet<ChargeUserPage>('/api/v1/admin/charge-users', query)
      .then(result => { if (current === generation.current) setPage(result); })
      .catch(e => { if (current === generation.current) setError(axios.isAxiosError<ApiEnvelope>(e) ? e.message : '加载失败，请稍后重试'); })
      .finally(() => { if (current === generation.current) setLoading(false); });
  }, [query, reload]);

  useEffect(() => {
    if (selected == null) return;
    setDetail(null); setDetailError(null); setDetailLoading(true);
    apiGet<ChargeUserDetail>(`/api/v1/admin/charge-users/${selected}`)
      .then(setDetail)
      .catch(e => setDetailError(axios.isAxiosError<ApiEnvelope>(e) ? e.message : '加载失败，请稍后重试'))
      .finally(() => setDetailLoading(false));
  }, [selected, detailReload]);

  return <div className="page-container">
    <Form form={form} layout="inline" initialValues={filters} style={{ rowGap: 12, marginBottom: 20 }}
      onFinish={values => { setFilters(values); setQuery(value => ({ ...value, page: 1, ...values })); }}>
      <Form.Item name="keyword" label="关键词">
        <Input allowClear maxLength={128} style={{ width: 300 }} placeholder="昵称、openid 或完整手机号" />
      </Form.Item>
      <Form.Item name="status" label="状态">
        <Select allowClear style={{ width: 130 }} placeholder="全部状态"
          options={Object.entries(statuses).map(([value, status]) => ({ value, label: status.label }))} />
      </Form.Item>
      <Form.Item>
        <Space>
          <Button type="primary" htmlType="submit" icon={<SearchOutlined />}>查询</Button>
          <Button onClick={() => { form.resetFields(); setFilters({ keyword: '', status: '' }); setQuery(value => ({ ...value, page: 1, keyword: '', status: '' })); }}>重置</Button>
        </Space>
      </Form.Item>
    </Form>
    <Typography.Paragraph type="secondary">
      手机号按完整号码精确查询：库中只存密文与不可逆哈希，没有可用于模糊匹配的明文，输入后四位之类的片段查不出来。
    </Typography.Paragraph>
    {error && <LoadError title="充电用户加载失败" detail={error} onRetry={() => setReload(value => value + 1)} />}
    <Table<ChargeUser> size="middle" rowKey="id" loading={loading} dataSource={page?.items || []} scroll={{ x: 1200 }}
      locale={{ emptyText: error ? '暂时无法获取充电用户' : '当前条件下没有充电用户' }}
      pagination={{ ...TABLE_PAGINATION, current: query.page, pageSize: query.page_size, total: page?.total || 0,
        showTotal: total => `共 ${total} 位用户`,
        onChange: (page, page_size) => setQuery(value => ({ ...value, page: page_size !== value.page_size ? 1 : page, page_size })) }}
      expandable={{ expandedRowRender: chargeUser => <Space direction="vertical" size={2}>
        <Typography.Text type="secondary">用户 ID {chargeUser.id} · openid {chargeUser.openid}{chargeUser.union_id ? ` · unionid ${chargeUser.union_id}` : ''}</Typography.Text>
        <Typography.Text type="secondary">首次出现 {time(chargeUser.first_seen_at)}{chargeUser.inviter_id ? ` · 邀请人 #${chargeUser.inviter_id}` : ' · 自然注册'}</Typography.Text>
      </Space> }}
      columns={[
        { title: '用户', key: 'user', width: 200, fixed: 'left', render: (_, chargeUser) => <Space>
          <Avatar size="small" src={chargeUser.avatar_url || undefined} icon={<SearchOutlined />} />
          <Button type="link" style={{ padding: 0 }} onClick={() => setSelected(chargeUser.id)}>{name(chargeUser)}</Button>
        </Space> },
        { title: '手机号', key: 'phone', width: 140, render: (_, chargeUser) => <Phone chargeUser={chargeUser} /> },
        { title: '状态', dataIndex: 'status', width: 100, render: statusTag },
        { title: '订单数', dataIndex: 'order_count', width: 90, align: 'right' },
        { title: '累计消费', dataIndex: 'total_cents', width: 120, align: 'right', render: money },
        { title: '钱包余额', dataIndex: 'balance_cents', width: 120, align: 'right', render: (value: number, chargeUser) =>
          chargeUser.frozen_cents ? <Space direction="vertical" size={0}>
            <span>{money(value)}</span>
            <Typography.Text type="secondary" style={{ fontSize: 12 }}>冻结 {money(chargeUser.frozen_cents)}</Typography.Text>
          </Space> : money(value) },
        { title: '最近下单', dataIndex: 'last_order_at', width: 160, render: time },
        { title: '最后登录', dataIndex: 'last_login_at', width: 160, render: time },
        { title: '操作', key: 'actions', width: 100, render: (_, chargeUser) => <Button size="small" onClick={() => setSelected(chargeUser.id)}>档案</Button> },
      ]} />
    <Drawer title={detail ? name(detail) : '充电用户档案'} open={selected != null} onClose={() => setSelected(null)} width="min(760px, 100vw)">
      {detailLoading && <Spin />}
      {detailError && <LoadError title="档案加载失败" detail={detailError} onRetry={() => setDetailReload(v => v + 1)} />}
      {detail && <Space direction="vertical" size="large" style={{ width: '100%' }}>
        <Descriptions title="账号" bordered column={2} items={[
          { key: 'id', label: '用户 ID', children: detail.id },
          { key: 'status', label: '状态', children: statusTag(detail.status) },
          { key: 'phone', label: '手机号', children: <Phone chargeUser={detail} />, span: 2 },
          { key: 'openid', label: 'openid', children: detail.openid, span: 2 },
          { key: 'unionid', label: 'unionid', children: detail.union_id || '—' },
          { key: 'gender', label: '性别', children: genders[detail.gender] || detail.gender },
          { key: 'first', label: '首次出现', children: time(detail.first_seen_at) },
          { key: 'last', label: '最后登录', children: time(detail.last_login_at) },
          { key: 'inviter', label: '邀请人', children: detail.inviter_id ? `#${detail.inviter_id}` : '自然注册' },
        ]} />
        <Descriptions title="钱包与消费" bordered column={2} items={[
          { key: 'balance', label: '可用余额', children: money(detail.balance_cents) },
          { key: 'wallet', label: '钱包状态', children: walletStatuses[detail.wallet_status] || detail.wallet_status },
          { key: 'frozen', label: '冻结金额', children: money(detail.frozen_cents) },
          { key: 'lastorder', label: '最近下单', children: time(detail.last_order_at) },
          { key: 'orders', label: '历史订单', children: `${detail.order_count} 笔` },
          { key: 'total', label: '累计消费', children: money(detail.total_cents) },
          { key: 'coupon', label: '优惠券', children: `累计发放 ${detail.coupon_granted} 张，未使用 ${detail.coupon_unused} 张` },
          { key: 'fault', label: '报障单', children: `${detail.fault_reports} 单` },
        ]} />
        <Typography.Title level={5}>最近订单</Typography.Title>
        {!detail.recent_orders.length && <Typography.Text type="secondary">该用户暂无充电订单</Typography.Text>}
        <Table<ChargeUserOrder> rowKey="order_id" size="middle" pagination={TABLE_PAGINATION} dataSource={detail.recent_orders}
          locale={{ emptyText: '暂无订单' }} columns={[
            { title: '订单号', dataIndex: 'order_no' },
            { title: '设备', dataIndex: 'device_id' },
            { title: '状态', dataIndex: 'status', render: value => orderStatuses[value] || value },
            { title: '金额', dataIndex: 'total_cents', align: 'right', render: value => value == null ? '待结算' : money(value) },
            { title: '下单时间', dataIndex: 'created_at', render: time },
          ]} />
      </Space>}
    </Drawer>
  </div>;
}
