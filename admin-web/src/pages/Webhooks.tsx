import { useCallback, useEffect, useState } from 'react';
import { Table, Typography, Tag, Space, Button, Modal, Form, Input, Select, Alert, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';

const { Title } = Typography;

interface Webhook { id: number; name: string; url: string; enabled: boolean; event_types: string[]; secret_prefix: string; }

interface Delivery {
  id: number;
  subscription_id: number;
  event_id: string;
  event_type: string;
  response_status: number | null;
  response_body: string | null;
  error_msg: string | null;
  attempt_count: number;
  duration_ms: number | null;
  delivered_at: string;
}

export default function WebhooksPage() {
  const [data, setData] = useState<Webhook[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [newSecret, setNewSecret] = useState('');
  const [log, setLog] = useState<Delivery[]>([]);
  const [logFor, setLogFor] = useState<Webhook | null>(null);
  const [logLoading, setLogLoading] = useState(false);
  const [resending, setResending] = useState<number | null>(null);
  const [form] = Form.useForm();

  const loadLog = useCallback(async (sub: Webhook) => {
    setLogLoading(true);
    try {
      const data = await apiGet<{ items: Delivery[] }>(`/api/v1/admin/webhooks/${sub.id}/deliveries?page=1&page_size=50`);
      setLog(data.items || []);
    } catch (e: any) {
      message.error(e?.message || '投递日志读取失败');
    } finally {
      setLogLoading(false);
    }
  }, []);

  const resend = async (row: Delivery) => {
    if (!logFor) return;
    setResending(row.id);
    try {
      await apiPost(`/api/v1/admin/webhooks/${logFor.id}/deliveries/${encodeURIComponent(row.event_id)}/retry`, {});
      message.success('已重新入队，稍后刷新查看结果');
      await new Promise((resolve) => setTimeout(resolve, 1500));
      await loadLog(logFor);
    } catch (e: any) {
      message.error(e?.message || '重发失败');
    } finally {
      setResending(null);
    }
  };

  const load = async () => {
    setLoading(true);
    try { setData((await apiGet<{ items: Webhook[] }>('/api/v1/admin/webhooks')).items || []); }
    catch (error: any) { message.error(error?.message || 'Webhook 列表读取失败'); } finally { setLoading(false); }
  };

  useEffect(() => { load(); }, []);

  const onCreate = async () => {
    try {
      const v = await form.validateFields();
      const result = await apiPost<{ secret?: string }>('/api/v1/admin/webhooks', v);
      message.success('已创建'); setNewSecret(result.secret || ''); setOpen(false); form.resetFields(); await load();
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
          { title: '签名密钥', dataIndex: 'secret_prefix', render: (value: string) => value ? `${value}…` : '—' },
          { title: '状态', dataIndex: 'enabled', render: (e: boolean) =>
            <Tag color={e ? 'green' : 'default'}>{e ? '启用' : '禁用'}</Tag> },
          { title: '操作', width: 110, render: (_, row) =>
            <Button type="link" onClick={() => { setLogFor(row); void loadLog(row); }}>投递日志</Button> },
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
      <Modal title="请立即复制签名密钥" open={!!newSecret} onCancel={() => setNewSecret('')} footer={[
        <Button key="copy" type="primary" onClick={async () => { try { await navigator.clipboard.writeText(newSecret); message.success('密钥已复制'); } catch { message.error('复制失败，请手动复制'); } }}>复制密钥</Button>,
        <Button key="close" onClick={() => setNewSecret('')}>完成</Button>,
      ]}>
        <Alert type="warning" showIcon message="关闭后不会再次显示明文密钥，请将它安全保存。" />
        <Input value={newSecret} readOnly style={{ marginTop: 12 }} aria-label="新建 Webhook 签名密钥" />
      </Modal>
      <Modal title={`${logFor?.name || ''} 投递日志`} open={!!logFor} onCancel={() => setLogFor(null)}
        footer={null} width={1000}>
        <Alert type="info" showIcon style={{ marginBottom: 12 }}
          message="每次投递都带 X-ChargePilot-Signature（HMAC-SHA256，签名覆盖「时间戳.请求体」），请据此校验来源。" />
        {log.length === 0 && !logLoading ? (
          <Alert type="info" showIcon message="暂无投递记录"
            description="订阅创建后，匹配事件由 worker 异步投递；每次投递（含失败）都会在此留痕。" />
        ) : null}
        <Table<Delivery> rowKey="id" size="small" loading={logLoading} dataSource={log} scroll={{ x: 900 }} pagination={false}
          columns={[
            { title: '事件', dataIndex: 'event_type', width: 140 },
            { title: '事件 ID', dataIndex: 'event_id', width: 190, ellipsis: true },
            { title: '响应', dataIndex: 'response_status', width: 90,
              render: (v: number | null) => v == null ? <Tag color="red">未送达</Tag>
                : <Tag color={v >= 200 && v < 300 ? 'green' : 'red'}>{v}</Tag> },
            { title: '次数', dataIndex: 'attempt_count', width: 70 },
            { title: '耗时', dataIndex: 'duration_ms', width: 90, render: (v: number | null) => v == null ? '—' : `${v} ms` },
            { title: '错误', dataIndex: 'error_msg', ellipsis: true },
            { title: '时间', dataIndex: 'delivered_at', width: 180 },
            { title: '操作', width: 90, render: (_, row) =>
              <Button type="link" onClick={() => void resend(row)} loading={resending === row.id}>重发</Button> },
          ]} />
      </Modal>
    </div>
  );
}
