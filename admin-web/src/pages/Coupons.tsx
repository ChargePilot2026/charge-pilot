import { useCallback, useEffect, useState } from 'react';
import { Tabs, Table, Typography, Tag, Space, Button, Modal, Form, Input, InputNumber, Select, DatePicker, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost, apiPut } from '../api/client';
import { LoadError } from '../components/LoadError';
import { TABLE_PAGINATION, useTablePagination } from '../utils/tablePagination';

const { Title } = Typography;

interface Coupon { id: number; name: string; discount_type: string;
  discount_value_cents?: number; discount_percent?: number;
  min_charge_cents: number; total_quota: number; per_user_quota: number; status: string; }

export default function CouponsPage() {
  const [data, setData] = useState<Coupon[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [grantCoupon, setGrantCoupon] = useState<Coupon | null>(null);
  const [editCoupon, setEditCoupon] = useState<Coupon | null>(null);
  const [couponStats, setCouponStats] = useState<any>(null);
  const [statsError, setStatsError] = useState<string | null>(null);
  // 记住正在看哪张券的统计，否则失败后没有可重试的目标。
  const [statsTarget, setStatsTarget] = useState<Coupon | null>(null);
  const [form] = Form.useForm();
  const [grantForm] = Form.useForm();
  const [loadError, setLoadError] = useState<string | null>(null);
  const [editForm] = Form.useForm();

  const load = async () => {
    setLoading(true);
    setLoadError(null);
    try { setData((await apiGet<{ items: Coupon[] }>('/api/v1/admin/coupons')).items || []); }
    catch (error: any) { setLoadError(error?.message || '优惠券读取失败'); } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      if (v.discount_type === 'amount') { v.discount_percent = undefined; v.free_minutes = undefined; }
      else if (v.discount_type === 'percentage') { v.discount_value_cents = undefined; v.free_minutes = undefined; }
      else { v.discount_percent = undefined; v.discount_value_cents = undefined; }
      if (!v.start_at) delete v.start_at; if (!v.end_at) delete v.end_at;
      await apiPost('/api/v1/admin/coupons', v);
      message.success('已创建'); setOpen(false); form.resetFields(); load();
    } catch (e: any) { if (e?.errorFields) return; message.error(e?.message || '失败'); }
  };

  const onGrant = async () => {
    try {
      const values = await grantForm.validateFields();
      const storageKey = `cp_coupon_grant:${grantCoupon!.id}:${values.user_id}`;
      const requestId = localStorage.getItem(storageKey) || crypto.randomUUID();
      localStorage.setItem(storageKey, requestId);
      await apiPost(`/api/v1/admin/coupons/${grantCoupon!.id}/grants`, { request_id: requestId, user_id: values.user_id });
      localStorage.removeItem(storageKey);
      message.success('优惠券已发放'); setGrantCoupon(null); grantForm.resetFields(); load();
    } catch (error: any) { if (error?.errorFields) return; message.error(error?.message || '发券失败；可用相同请求重试'); }
  };

  const onUpdate = async () => {
    try {
      const v = await editForm.validateFields();
      await apiPut(`/api/v1/admin/coupons/${editCoupon!.id}`, v);
      message.success('优惠券已更新'); setEditCoupon(null); load();
    } catch (error: any) { if (error?.errorFields) return; message.error(error?.message || '更新失败'); }
  };

  const showStats = async (coupon: Coupon) => {
    setStatsError(null);
    setStatsTarget(coupon);
    try { setCouponStats(await apiGet(`/api/v1/admin/coupons/${coupon.id}/stats`)); }
    catch (error: any) { setStatsError(error?.message || '统计读取失败'); }
  };

  return (
    <div className="page-container">
      <Tabs items={[
        { key: 'coupons', label: '优惠券', children: (
      <>
      <Space style={{ marginBottom: 12 }}>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建</Button>
      </Space>
      {loadError && <LoadError title="优惠券列表加载失败" detail={loadError} onRetry={() => void load()} />}
      <Table size="middle" rowKey="id" loading={loading} dataSource={data} pagination={TABLE_PAGINATION}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '类型', dataIndex: 'discount_type', width: 100 },
          { title: '优惠', dataIndex: 'discount_value_cents',
            render: (v: number | undefined, r: Coupon) => v != null ? `${v} 分` : (r.discount_percent != null ? `${r.discount_percent}%` : '-') },
          { title: '门槛', dataIndex: 'min_charge_cents', width: 100,
            render: (v: number) => `${v / 100} 元` },
          { title: '额度', key: 'quota', width: 120, render: (_: unknown, r: Coupon) => `${r.total_quota || '不限'} / 每人 ${r.per_user_quota}` },
          { title: '状态', dataIndex: 'status', width: 100,
            render: (s: string) => <Tag color={s === 'active' ? 'green' : 'default'}>{s}</Tag> },
          { title: '操作', key: 'actions', width: 260, render: (_: unknown, row: Coupon) => <Space>
            <Button size="small" onClick={() => { setEditCoupon(row); editForm.setFieldsValue({ name: row.name, status: row.status }); }}>编辑</Button>
            <Button size="small" onClick={() => showStats(row)}>统计</Button>
            <Button size="small" disabled={row.status !== 'active'} onClick={() => setGrantCoupon(row)}>发放</Button>
          </Space> },
        ]}
      />
      <Modal title="新建优惠券" open={open} onCancel={() => setOpen(false)} onOk={onCreate}>
        <Form name="coupon_create" form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="discount_type" label="类型" rules={[{ required: true }]}>
            <Select options={[
              { value: 'amount', label: '满减' },
              { value: 'percentage', label: '百分比折扣' },
              { value: 'time_free', label: '免费时长' },
            ]} />
          </Form.Item>
          <Form.Item name="discount_value_cents" label="满减值(分)"><InputNumber style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="discount_percent" label="折扣百分比"><InputNumber style={{ width: '100%' }} step="0.1" /></Form.Item>
          <Form.Item name="free_minutes" label="免费时长（分钟，仅免费时长类型）"><InputNumber min={1} max={1440} precision={0} /></Form.Item>
          <Form.Item name="min_charge_cents" label="最低消费(分)" initialValue={0}><InputNumber style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="valid_hours" label="有效小时" initialValue={24}><InputNumber style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="total_quota" label="总发放量（0 表示不限）" initialValue={0}><InputNumber min={0} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="per_user_quota" label="每位用户最多领取" initialValue={1}><InputNumber min={1} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="start_at" label="开始时间（可选，ISO 8601）"><Input placeholder="2026-10-01T00:00:00Z" /></Form.Item>
          <Form.Item name="end_at" label="结束时间（可选，ISO 8601）"><Input placeholder="2026-12-31T23:59:59Z" /></Form.Item>
        </Form>
      </Modal>
      <Modal title={`发放优惠券${grantCoupon ? `：${grantCoupon.name}` : ''}`} open={!!grantCoupon} onCancel={() => { setGrantCoupon(null); grantForm.resetFields(); }} onOk={onGrant}>
        <Form name="coupon_grant" form={grantForm} layout="vertical">
          <Form.Item name="user_id" label="用户编号" rules={[{ required: true }, { type: 'number', min: 1 }]}><InputNumber precision={0} style={{ width: '100%' }} /></Form.Item>
          <Typography.Text type="secondary">服务端会检查用户状态、模板有效期、总发放量和每人额度。网络结果不确定时保持此弹窗并重试，避免重复发券。</Typography.Text>
        </Form>
      </Modal>
      <Modal title={`编辑优惠券${editCoupon ? `：${editCoupon.name}` : ''}`} open={!!editCoupon} onCancel={() => setEditCoupon(null)} onOk={onUpdate}>
        <Form name="coupon_edit" form={editForm} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="status" label="状态" rules={[{ required: true }]}><Select options={[{ value: 'active', label: '启用' }, { value: 'disabled', label: '停用' }]} /></Form.Item>
        </Form>
      </Modal>
      <Modal title="优惠券发放统计" open={couponStats !== null || statsError !== null} onCancel={() => { setCouponStats(null); setStatsError(null); setStatsTarget(null); }} footer={null}>
        {statsError && <LoadError title="发放统计加载失败" detail={statsError} onRetry={() => statsTarget && void showStats(statsTarget)} />}
        {couponStats && <Space direction="vertical">
          <Typography.Text>总额度：{couponStats.total_quota || '不限'}</Typography.Text>
          <Typography.Text>已发放：{couponStats.granted_count}</Typography.Text>
          <Typography.Text>可用：{couponStats.unused_count}</Typography.Text>
          <Typography.Text>已使用：{couponStats.used_count}</Typography.Text>
          <Typography.Text>已过期：{couponStats.expired_count}</Typography.Text>
          <Typography.Text>使用率：{(Number(couponStats.usage_rate || 0) * 100).toFixed(1)}%</Typography.Text>
        </Space>}
      </Modal>
      </>
        ) },
        { key: 'activities', label: '活动规则', children: <ActivityRules coupons={data} reloadCoupons={load} /> },
      ]} />
    </div>
  );
}

const triggerLabel: Record<string, string> = {
  first_recharge: '首充优惠', invite_reward: '邀请有奖', threshold_redeem: '满减满返', holiday: '节日活动',
};

// 活动规则在这里配置而不是手写 SQL，这样活动窗口和预算就能在系统跑着的时候改。
function ActivityRules({ coupons, reloadCoupons }: { coupons: Coupon[]; reloadCoupons: () => void }) {
  const [rows, setRows] = useState<any[]>([]);
  const [total, setTotal] = useState(0);
  const { pagination, tablePagination } = useTablePagination();
  const [loadError, setLoadError] = useState<string | null>(null);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  const trigger = Form.useWatch('trigger_type', form);

  const load = useCallback(async () => {
    setLoading(true);
    setLoadError(null);
    try {
      const result = await apiGet<{ items: any[]; total: number }>('/api/v1/admin/coupon-activities', pagination);
      setRows(result.items || []); setTotal(result.total);
    }
    catch (e: any) { setLoadError(e?.message || '活动规则读取失败'); } finally { setLoading(false); }
  }, [pagination]);

  useEffect(() => { void load(); }, [load]);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      // 接口说的是 RFC3339，而选择器交回来的是 dayjs 对象。
      const body = {
        ...v,
        start_at: v.start_at.toISOString(),
        end_at: v.end_at.toISOString(),
        inviter_coupon_id: v.inviter_coupon_id ?? undefined,
      };
      await apiPost('/api/v1/admin/coupon-activities', body);
      message.success('已创建活动规则');
      setOpen(false); form.resetFields(); void load();
    } catch (e: any) { if (e?.errorFields) return; message.error(e?.message || '创建失败'); }
  };

  const setStatus = async (row: any, status: string) => {
    try {
      await apiPut(`/api/v1/admin/coupon-activities/${row.id}`, {
        name: row.name, trigger_type: row.trigger_type, coupon_id: row.coupon_id,
        inviter_coupon_id: row.inviter_coupon_id ?? undefined, threshold_cents: row.threshold_cents,
        max_grants: row.max_grants, per_user_limit: row.per_user_limit, status,
        start_at: new Date(row.start_at).toISOString(), end_at: new Date(row.end_at).toISOString(),
      });
      message.success(status === 'active' ? '已启用' : '已停用');
      void load();
    } catch (e: any) { message.error(e?.message || '操作失败'); }
  };

  const couponOptions = coupons.filter(c => c.status === 'active').map(c => ({ value: c.id, label: c.name }));

  return (
    <Space direction="vertical" style={{ width: '100%' }}>
      <Space>
        <Title level={3} style={{ margin: 0 }}>活动规则</Title>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建活动</Button>
      </Space>
      <Typography.Text type="secondary">
        规则在触发事实发生的同一事务内评估。满减未达门槛、活动过期、预算耗尽都不会发放，也不会影响支付或订单本身。
      </Typography.Text>
      {loadError && <LoadError title="活动规则加载失败" detail={loadError} onRetry={() => void load()} />}
      <Table size="middle" rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 1000 }} pagination={{ ...tablePagination, total }}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '触发', dataIndex: 'trigger_type', width: 110, render: (v: string) => triggerLabel[v] || v },
          { title: '活动券', dataIndex: 'coupon_name', width: 180 },
          { title: '门槛', dataIndex: 'threshold_cents', width: 110,
            render: (v: number, r: any) => r.trigger_type === 'threshold_redeem' ? `${v / 100} 元` : '—' },
          { title: '预算 / 单人', key: 'quota', width: 130,
            render: (_: unknown, r: any) => `${r.max_grants || '不限'} / 每人 ${r.per_user_limit}` },
          { title: '已发放', dataIndex: 'granted_count', width: 90 },
          { title: '窗口', key: 'window', width: 200,
            render: (_: unknown, r: any) => `${new Date(r.start_at).toLocaleDateString()} → ${new Date(r.end_at).toLocaleDateString()}` },
          { title: '状态', dataIndex: 'status', width: 90,
            render: (s: string) => <Tag color={s === 'active' ? 'green' : 'default'}>{s === 'active' ? '启用' : '停用'}</Tag> },
          { title: '操作', key: 'actions', width: 140, render: (_: unknown, r: any) =>
            r.status === 'active'
              ? <Button size="small" onClick={() => void setStatus(r, 'disabled')}>停用</Button>
              : <Button size="small" onClick={() => void setStatus(r, 'active')}>启用</Button> },
        ]} />
      <Modal title="新建活动" open={open} onCancel={() => setOpen(false)} onOk={() => void onCreate()} confirmLoading={saving}>
        <Form form={form} layout="vertical" initialValues={{ trigger_type: 'first_recharge', per_user_limit: 1, max_grants: 0, threshold_cents: 0 }}>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="trigger_type" label="触发方式" rules={[{ required: true }]}>
            <Select options={Object.entries(triggerLabel).map(([value, label]) => ({ value, label }))} />
          </Form.Item>
          <Form.Item name="coupon_id" label="活动券" rules={[{ required: true, message: '请选择活动券' }]}>
            <Select options={couponOptions} placeholder={couponOptions.length ? '选择一张已启用的券' : '请先在“优惠券”页创建并启用一张券'} />
          </Form.Item>
          {trigger === 'invite_reward' && (
            <Form.Item name="inviter_coupon_id" label="邀请人奖励券" rules={[{ required: true, message: '邀请有奖必须设置邀请人奖励' }]}>
              <Select options={couponOptions} />
            </Form.Item>
          )}
          {trigger === 'threshold_redeem' && (
            <Form.Item name="threshold_cents" label="订单门槛（分）" rules={[{ required: true }]}>
              <InputNumber min={1} style={{ width: '100%' }} placeholder="例如 3000 表示满 30 元" />
            </Form.Item>
          )}
          <Form.Item name="max_grants" label="活动总发放上限" extra="0 表示不限">
            <InputNumber min={0} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="per_user_limit" label="每人上限" rules={[{ required: true }]}>
            <InputNumber min={1} style={{ width: '100%' }} />
          </Form.Item>
          <Form.Item name="start_at" label="开始时间" rules={[{ required: true }]}><DatePicker showTime style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="end_at" label="结束时间" rules={[{ required: true }]}><DatePicker showTime style={{ width: '100%' }} /></Form.Item>
        </Form>
      </Modal>
    </Space>
  );
}
