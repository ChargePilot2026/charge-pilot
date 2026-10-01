import { TABLE_PAGINATION } from '../utils/tablePagination';
import { useEffect, useState } from 'react';
import { Button, Form, Input, InputNumber, Modal, Space, Switch, Table, Tag, Typography, message } from 'antd';
import { MessageOutlined, PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiDelete, apiGet, apiPost, apiPut } from '../api/client';
import { LoadError } from '../components/LoadError';

const { Text } = Typography;
const endpoint = '/api/v1/admin/customer-service';

interface Seat {
  id: number;
  agent_wechat: string;
  agent_name: string | null;
  path: string | null;
  priority: number;
  enabled: boolean;
  working_hours_json?: unknown;
}

export default function CustomerServicePage() {
  const [items, setItems] = useState<Seat[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [open, setOpen] = useState(false);
  const [editing, setEditing] = useState<number | null>(null);
  const [listError, setListError] = useState<string | null>(null);
  const [form] = Form.useForm();

  const load = async () => {
    setLoading(true);
    try {
      const data = await apiGet<{ items: Seat[] }>(endpoint);
      setItems(Array.isArray(data?.items) ? data.items : []);
      setListError(null);
    } catch (error: any) {
      setItems([]); setListError(error?.message || '客服配置读取失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { void load(); }, []);

  const create = () => {
    setEditing(null);
    form.setFieldsValue({ agent_wechat: '', agent_name: '', path: '', priority: 0, enabled: true });
    setOpen(true);
  };

  const edit = (seat: Seat) => {
    setEditing(seat.id);
    form.setFieldsValue({ ...seat, path: seat.path || '', agent_name: seat.agent_name || '' });
    setOpen(true);
  };

  const save = async () => {
    try {
      const value = await form.validateFields();
      setSaving(true);
      const payload = { ...value, path: value.path?.trim() || null, agent_name: value.agent_name?.trim() || null };
      if (editing === null) await apiPost(endpoint, payload);
      else await apiPut(`${endpoint}/${editing}`, payload);
      message.success(editing === null ? '客服坐席已添加' : '客服坐席已更新');
      setOpen(false);
      await load();
    } catch (error: any) {
      if (!error?.errorFields) message.error(error?.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const disable = (seat: Seat) => {
    Modal.confirm({
      title: '停用客服坐席',
      content: `停用 ${seat.agent_name || seat.agent_wechat} 后，小程序不再把新会话分配给该坐席。`,
      okText: '停用',
      okButtonProps: { danger: true },
      onOk: async () => {
        try { await apiDelete(`${endpoint}/${seat.id}`); message.success('坐席已停用'); await load(); }
        catch (error: any) { message.error(error?.message || '停用失败'); }
      },
    });
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Button icon={<ReloadOutlined />} onClick={() => void load()}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} onClick={create}>添加坐席</Button>
      </Space>
      <Text type="secondary" style={{ display: 'block', marginBottom: 16 }}>
        小程序客服入口使用优先级最高的启用坐席。微信原生客服还需要配置 WECHAT_CUSTOMER_SERVICE_CORP_ID 和 HTTPS 客服链接。
      </Text>
      {listError && <LoadError title="客服坐席加载失败" detail={listError} onRetry={load} />}
      <Table pagination={TABLE_PAGINATION} size="middle" rowKey="id" loading={loading} dataSource={items} columns={[
        { title: '坐席名称', dataIndex: 'agent_name', render: (name: string | null, seat: Seat) => name || seat.agent_wechat },
        { title: '微信号', dataIndex: 'agent_wechat' },
        { title: '客服入口', dataIndex: 'path', render: (path: string | null) => path ? <a href={path} target="_blank" rel="noreferrer">打开链接</a> : <Text type="secondary">未配置</Text> },
        { title: '优先级', dataIndex: 'priority', width: 100 },
        { title: '状态', dataIndex: 'enabled', width: 100, render: (enabled: boolean) => <Tag color={enabled ? 'green' : 'default'}>{enabled ? '启用' : '停用'}</Tag> },
        { title: '操作', key: 'actions', width: 180, render: (_: unknown, seat: Seat) => <Space><Button type="link" icon={<MessageOutlined />} onClick={() => edit(seat)}>编辑</Button>{seat.enabled && <Button type="link" danger onClick={() => disable(seat)}>停用</Button>}</Space> },
      ]} />
      <Modal title={editing === null ? '添加客服坐席' : '编辑客服坐席'} open={open} onCancel={() => setOpen(false)} onOk={() => void save()} confirmLoading={saving} destroyOnHidden>
        <Form form={form} layout="vertical" initialValues={{ priority: 0, enabled: true }}>
          <Form.Item name="agent_name" label="坐席名称"><Input maxLength={64} /></Form.Item>
          <Form.Item name="agent_wechat" label="客服微信号" rules={[{ required: true, whitespace: true, max: 64 }]}><Input maxLength={64} /></Form.Item>
          <Form.Item name="path" label="微信客服 HTTPS 入口" rules={[{ type: 'url', warningOnly: true }, { validator: async (_, value) => { if (value && !value.startsWith('https://')) throw new Error('入口必须使用 HTTPS'); } }]}><Input maxLength={512} placeholder="https://work.weixin.qq.com/..." /></Form.Item>
          <Form.Item name="priority" label="优先级" rules={[{ required: true }]}><InputNumber min={0} max={4294967295} precision={0} style={{ width: '100%' }} /></Form.Item>
          <Form.Item name="enabled" label="启用状态" valuePropName="checked"><Switch checkedChildren="启用" unCheckedChildren="停用" /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
