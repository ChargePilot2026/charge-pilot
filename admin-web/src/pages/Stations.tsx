import { useEffect, useState } from 'react';
import { Table, Typography, Space, Button, Modal, Form, Input, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';

const { Title } = Typography;

interface Station {
  id: number; code: string; name: string;
  address?: string; longitude: number; latitude: number;
  status: string;
}

export default function StationsPage() {
  const [data, setData] = useState<Station[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try { setData((await apiGet<{ items: Station[] }>('/api/v1/admin/stations')).items || []); }
    catch { setData([]); } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      await apiPost('/api/v1/admin/stations', v);
      message.success('已创建');
      setOpen(false); form.resetFields(); load();
    } catch (e: any) {
      if (e?.errorFields) return;
      message.error(e?.message || '失败');
    }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>站点</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建</Button>
      </Space>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={data}
        columns={[
          { title: '编码', dataIndex: 'code', width: 120 },
          { title: '名称', dataIndex: 'name' },
          { title: '地址', dataIndex: 'address' },
          { title: '经度', dataIndex: 'longitude', width: 120 },
          { title: '纬度', dataIndex: 'latitude', width: 120 },
          { title: '状态', dataIndex: 'status', width: 100 },
        ]}
      />
      <Modal title="新建站点" open={open} onCancel={() => setOpen(false)} onOk={onCreate} okText="创建" cancelText="取消">
        <Form form={form} layout="vertical">
          <Form.Item name="code" label="编码" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="address" label="地址"><Input /></Form.Item>
          <Form.Item name="longitude" label="经度" rules={[{ required: true }]}><Input type="number" /></Form.Item>
          <Form.Item name="latitude" label="纬度" rules={[{ required: true }]}><Input type="number" /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
}