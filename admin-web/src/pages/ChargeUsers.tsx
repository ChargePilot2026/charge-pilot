import { Avatar, Button, Form, Input, Select, Space, Table, Typography } from 'antd';
import { SearchOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';
import axios from 'axios';
import { ApiEnvelope, apiGet } from '../api/client';
import { LoadError } from '../components/LoadError';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../utils/tablePagination';
import { isChargeUserID } from '../utils/chargeUserID';
import ChargeUserProfileDrawer, {
  type ChargeUser, ChargeUserPhone as Phone, chargeUserName as name,
  chargeUserStatuses as statuses, chargeUserStatusTag as statusTag,
  chargeUserTime as time, chargeUserMoney as money,
} from './chargeUsers/ChargeUserProfileDrawer';

interface ChargeUserPage { items: ChargeUser[]; total: number; page: number; page_size: number }

export default function ChargeUsersPage() {
  const [form] = Form.useForm();
  const [filters, setFilters] = useState({ keyword: '', status: '' });
  const [query, setQuery] = useState({ page: 1, page_size: DEFAULT_PAGE_SIZE, keyword: '', status: '' });
  const [page, setPage] = useState<ChargeUserPage | null>(null);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState<string | null>(null);
  const [reload, setReload] = useState(0);
  const [selected, setSelected] = useState<string | null>(null);
  const generation = useRef(0);

  useEffect(() => {
    const current = ++generation.current;
    setLoading(true); setError(null);
    apiGet<ChargeUserPage>('/api/v1/admin/charge-users', query)
      .then(result => {
        if (current !== generation.current) return;
        if (!Array.isArray(result?.items) || result.items.some(user => !isChargeUserID(user.id)
          || user.inviter_id != null && !isChargeUserID(user.inviter_id))) {
          throw new Error('用户编号响应格式不正确，请重新读取');
        }
        setPage(result);
      })
      .catch(e => { if (current === generation.current) setError(axios.isAxiosError<ApiEnvelope>(e) || e instanceof Error ? e.message : '加载失败，请稍后重试'); })
      .finally(() => { if (current === generation.current) setLoading(false); });
  }, [query, reload]);

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
      手机号支持完整号码或号码片段查询。
    </Typography.Paragraph>
    {error && <LoadError title="充电用户加载失败" detail={error} onRetry={() => setReload(value => value + 1)} />}
    <Table<ChargeUser> size="middle" rowKey="id" loading={loading} dataSource={page?.items || []} scroll={{ x: 1200 }}
      locale={{ emptyText: error ? '暂时无法获取充电用户' : '当前条件下没有充电用户' }}
      pagination={{ ...TABLE_PAGINATION, current: query.page, pageSize: query.page_size, total: page?.total || 0,
        showTotal: total => `共 ${total} 位用户`,
        onChange: (page, page_size) => setQuery(value => ({ ...value, page: page_size !== value.page_size ? 1 : page, page_size })) }}
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
    <ChargeUserProfileDrawer userID={selected} onClose={() => setSelected(null)} />
  </div>;
}
