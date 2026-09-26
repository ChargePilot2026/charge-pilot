import { useEffect, useState } from 'react';
import { Table, Typography, Tag, Space, Button, Modal, Form, Input, InputNumber, Select, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';

const { Title } = Typography;

interface Coupon { id: number; code: string; name: string; discount_type: string;
  discount_value_cents?: number; discount_percent?: number;
  min_charge_cents: number; status: string; }

export default function CouponsPage() {
  const [data, setData] = useState<Coupon[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try { setData((await apiGet<{ items: Coupon[] }>('/api/v1/admin/coupons')).items || []); }
    catch { setData([]); } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      await apiPost('/api/v1/admin/coupons', v);
      message.success('已创建'); setOpen(false); form.resetFields(); load();
    } catch (e: any) { if (e?.errorFields) return; message.error(e?.message || '失败'); }
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
          { title: '状态', dataIndex: 'status', width: 100,
            render: (s: string) => <Tag color={s === 'active' ? 'green' : 'default'}>{s}</Tag> },
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
        </Form>
      </Modal>
    </div>
  );
}
