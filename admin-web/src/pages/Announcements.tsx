import { TABLE_PAGINATION } from '../utils/tablePagination';
import { useEffect, useState } from 'react';
import { Table, Space, Button, Modal, Form, Input, DatePicker, Select, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';
import { LoadError } from '../components/LoadError';

interface Announcement { id: number; title: string; content: string; scope: string; status: string; }

export default function AnnouncementsPage() {
  const [data, setData] = useState<Announcement[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [loadError, setLoadError] = useState<string | null>(null);
  const [form] = Form.useForm();

  // 读失败不能吞成空列表：公告页一片空白，运营会以为没发过公告。
  const load = async () => {
    setLoading(true);
    try {
      setData((await apiGet<{ items: Announcement[] }>('/api/v1/admin/announcements')).items || []);
      setLoadError(null);
    } catch (e: any) {
      setData([]); setLoadError(e?.message || '公告列表读取失败');
    } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      const payload = {
        ...v,
        target_ids: v.scope === 'global' ? [] : (v.target_ids || '').split(',').map((s: string) => s.trim()).filter(Boolean),
        start_at: v.start_at?.toISOString(),
        end_at: v.end_at?.toISOString(),
      };
      await apiPost('/api/v1/admin/announcements', payload);
      message.success('公告已发布'); setOpen(false); form.resetFields(); load();
    } catch (e: any) { if (e?.errorFields) return; message.error(e?.message || '失败'); }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建</Button>
      </Space>
      {loadError && <LoadError title="公告列表加载失败" detail={loadError} onRetry={load} />}
      <Table pagination={TABLE_PAGINATION} size="middle" rowKey="id" loading={loading} dataSource={data}
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
          <Form.Item name="target_ids" label="目标站点 ID / 城市编码（逗号分隔；全局可留空）"><Input /></Form.Item>
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
