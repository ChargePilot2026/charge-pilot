import { useEffect, useState } from 'react';
import { Table, Typography, Space, Button, Modal, Form, Input, DatePicker, Select, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import dayjs from 'dayjs';
import { apiGet, apiPost } from '../api/client';

const { Title } = Typography;

interface Announcement { id: number; title: string; content: string; scope: string; status: string; }

export default function AnnouncementsPage() {
  const [data, setData] = useState<Announcement[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try { setData((await apiGet<{ items: Announcement[] }>('/api/v1/admin/announcements')).items || []); }
    catch { setData([]); } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      const payload = {
        ...v,
        start_at: v.start_at?.toISOString(),
        end_at: v.end_at?.toISOString(),
      };
      await apiPost('/api/v1/admin/announcements', payload);
      message.success('已创建'); setOpen(false); form.resetFields(); load();
    } catch (e: any) { if (e?.errorFields) return; message.error(e?.message || '失败'); }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>公告</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建</Button>
      </Space>
      <Table rowKey="id" loading={loading} dataSource={data}
        columns={[
          { title: '标题', dataIndex: 'title' },
          { title: '范围', dataIndex: 'scope', width: 120 },
          { title: '状态', dataIndex: 'status', width: 100 },
        ]}
      />
      <Modal title="新建公告" open={open} onCancel={() => setOpen(false)} onOk={onCreate}>
        <Form form={form} layout="vertical">
          <Form.Item name="title" label="标题" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="content" label="内容" rules={[{ required: true }]}><Input.TextArea rows={4} /></Form.Item>
          <Form.Item name="scope" label="范围" initialValue="global" rules={[{ required: true }]}>
            <Select options={[
              { value: 'global', label: '全局' },
              { value: 'station', label: '站点' },
              { value: 'city', label: '城市' },
            ]} />
          </Form.Item>
          <Form.Item name="start_at" label="开始时间" rules={[{ required: true }]}>
            <DatePicker showTime />
          </Form.Item>
          <Form.Item name="end_at" label="结束时间">
            <DatePicker showTime />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}