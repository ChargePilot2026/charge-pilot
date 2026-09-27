import { useEffect, useState } from 'react';
import { Table, Typography, Tag, Space, Button, Modal, Form, Input, InputNumber, Select, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost, apiPut } from '../api/client';

const { Title } = Typography;

interface Coupon { id: number; code: string; name: string; discount_type: string;
  discount_value_cents?: number; discount_percent?: number;
  min_charge_cents: number; total_quota: number; per_user_quota: number; status: string; }

export default function CouponsPage() {
  const [data, setData] = useState<Coupon[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [grantCoupon, setGrantCoupon] = useState<Coupon | null>(null);
  const [editCoupon, setEditCoupon] = useState<Coupon | null>(null);
  const [couponStats, setCouponStats] = useState<any>(null);
  const [form] = Form.useForm();
  const [grantForm] = Form.useForm();
  const [editForm] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try { setData((await apiGet<{ items: Coupon[] }>('/api/v1/admin/coupons')).items || []); }
    catch (error: any) { message.error(error?.message || '优惠券读取失败'); } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      if (v.discount_type === 'amount') v.discount_percent = undefined;
      else if (v.discount_type === 'percentage') v.discount_value_cents = undefined;
      else { v.discount_percent = undefined; v.discount_value_cents = undefined; }
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
    try { setCouponStats(await apiGet(`/api/v1/admin/coupons/${coupon.id}/stats`)); }
    catch (error: any) { message.error(error?.message || '统计读取失败'); }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>优惠券</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建</Button>
      </Space>
      <Table rowKey="id" loading={loading} dataSource={data}
        columns={[
          { title: '编码', dataIndex: 'code', width: 140 },
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
        <Form form={form} layout="vertical">
          <Form.Item name="code" label="编码" rules={[{ required: true }]}><Input /></Form.Item>
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
          <Form.Item name="min_charge_cents" label="最低消费(分)" initialValue={0}><InputNumber style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="valid_hours" label="有效小时" initialValue={24}><InputNumber style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="total_quota" label="总发放量（0 表示不限）" initialValue={0}><InputNumber min={0} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="per_user_quota" label="每位用户最多领取" initialValue={1}><InputNumber min={1} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="start_at" label="开始时间（可选，ISO 8601）"><Input placeholder="2026-10-01T00:00:00Z" /></Form.Item>
          <Form.Item name="end_at" label="结束时间（可选，ISO 8601）"><Input placeholder="2026-12-31T23:59:59Z" /></Form.Item>
        </Form>
      </Modal>
      <Modal title={`发放优惠券${grantCoupon ? `：${grantCoupon.name}` : ''}`} open={!!grantCoupon} onCancel={() => { setGrantCoupon(null); grantForm.resetFields(); }} onOk={onGrant}>
        <Form form={grantForm} layout="vertical">
          <Form.Item name="user_id" label="用户编号" rules={[{ required: true }, { type: 'number', min: 1 }]}><InputNumber precision={0} style={{ width: '100%' }} /></Form.Item>
          <Typography.Text type="secondary">服务端会检查用户状态、模板有效期、总发放量和每人额度。网络结果不确定时保持此弹窗并重试，避免重复发券。</Typography.Text>
        </Form>
      </Modal>
      <Modal title={`编辑优惠券${editCoupon ? `：${editCoupon.name}` : ''}`} open={!!editCoupon} onCancel={() => setEditCoupon(null)} onOk={onUpdate}>
        <Form form={editForm} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="status" label="状态" rules={[{ required: true }]}><Select options={[{ value: 'active', label: '启用' }, { value: 'disabled', label: '停用' }]} /></Form.Item>
        </Form>
      </Modal>
      <Modal title="优惠券发放统计" open={couponStats !== null} onCancel={() => setCouponStats(null)} footer={null}>
        {couponStats && <Space direction="vertical">
          <Typography.Text>总额度：{couponStats.total_quota || '不限'}</Typography.Text>
          <Typography.Text>已发放：{couponStats.granted_count}</Typography.Text>
          <Typography.Text>可用：{couponStats.unused_count}</Typography.Text>
          <Typography.Text>已使用：{couponStats.used_count}</Typography.Text>
          <Typography.Text>已过期：{couponStats.expired_count}</Typography.Text>
          <Typography.Text>使用率：{(Number(couponStats.usage_rate || 0) * 100).toFixed(1)}%</Typography.Text>
        </Space>}
      </Modal>
    </div>
  );
}
