import { useEffect, useState } from 'react';
import { Table, Typography, Tag, Space, Button, Modal, Form, Input, Select, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';

const { Title } = Typography;

interface Webhook { id: number; name: string; url: string; enabled: boolean; event_types: string[]; }

export default function WebhooksPage() {
  const [data, setData] = useState<Webhook[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try { setData((await apiGet<{ items: Webhook[] }>('/api/v1/admin/webhooks')).items || []); }
    catch { setData([]); } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      await apiPost('/api/v1/admin/webhooks', v);
      message.success('已创建'); setOpen(false); form.resetFields(); load();
    } catch (e: any) { if (e?.errorFields) return; message.error(e?.message || '失败'); }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>Webhook 订阅</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建</Button>
      </Space>
      <Table rowKey="id" loading={loading} dataSource={data}
        columns={[
          { title: '名称', dataIndex: 'name' },
          { title: 'URL', dataIndex: 'url', ellipsis: true },
          { title: '事件', dataIndex: 'event_types',
            render: (v: string[]) => v.map(t => <Tag key={t}>{t}</Tag>) },
          { title: '状态', dataIndex: 'enabled', render: (e: boolean) =>
            <Tag color={e ? 'green' : 'default'}>{e ? '启用' : '禁用'}</Tag> },
        ]}
      />
      <Modal title="新建 Webhook" open={open} onCancel={() => setOpen(false)} onOk={onCreate}>
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="url" label="URL" rules={[{ required: true, type: 'url' }]}><Input /></Form.Item>
          <Form.Item name="event_types" label="订阅事件" rules={[{ required: true }]}>
            <Select mode="multiple" options={[
              { value: 'alert', label: '告警' },
              { value: 'charge_ended', label: '充电结束' },
              { value: 'refund_completed', label: '退款完成' },
              { value: 'ota_completed', label: 'OTA 完成' },
            ]} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}