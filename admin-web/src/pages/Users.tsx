import { useEffect, useState } from 'react';
import { Table, Tag, Space, Button, Typography, Modal, Form, Input, Select, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';

const { Title } = Typography;

interface AdminUser { id: number; username: string; display_name?: string; role_id?: number; status: string; }

export default function UsersPage() {
  const [data, setData] = useState<AdminUser[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try { setData((await apiGet<{ items: AdminUser[] }>('/api/v1/admin/users')).items || []); }
    catch { setData([]); } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      await apiPost('/api/v1/admin/users', v);
      message.success('已创建'); setOpen(false); form.resetFields(); load();
    } catch (e: any) {
      if (e?.errorFields) return;
      message.error(e?.message || '失败');
    }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>管理员</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建</Button>
      </Space>
      <Table
        rowKey="id"
        loading={loading}
        dataSource={data}
        columns={[
          { title: '用户名', dataIndex: 'username' },
          { title: '显示名', dataIndex: 'display_name' },
          { title: '角色 ID', dataIndex: 'role_id' },
          { title: '状态', dataIndex: 'status',
            render: (s: string) => <Tag color={s === 'active' ? 'green' : 'red'}>{s}</Tag> },
        ]}
      />
      <Modal title="新建管理员" open={open} onCancel={() => setOpen(false)} onOk={onCreate}>
        <Form form={form} layout="vertical">
          <Form.Item name="username" label="用户名" rules={[{ required: true }]}><Input /></Form.Item>
          <Form.Item name="display_name" label="显示名"><Input /></Form.Item>
          <Form.Item name="password" label="密码" rules={[{ required: true, min: 8 }]}><Input.Password /></Form.Item>
          <Form.Item name="role_id" label="角色">
            <Select options={[
              { value: 1, label: '超级管理员' },
              { value: 2, label: '运营' },
              { value: 3, label: '客服' },
              { value: 4, label: '财务' },
            ]} />
          </Form.Item>
          <Form.Item name="phone" label="手机号"><Input /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
}