import { useCallback, useEffect, useState } from 'react';
import { Alert, App, Button, Form, Modal, Select, Space, Table, Tag, Typography } from 'antd';
import { DownloadOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';

const { Title, Text, Paragraph } = Typography;
const endpoint = '/api/v1/admin/exports';

interface ExportTask {
  id: number;
  task_no: string;
  resource: string;
  status: string;
  requested_by: number;
  row_count: number;
  error_msg: string | null;
  expires_at: string | null;
  created_at: string;
  completed_at: string | null;
}

const resourceLabel: Record<string, string> = {
  orders: '充电订单', stations: '站点', devices: '设备', settlements: '分账明细',
};
const statusMeta: Record<string, { color: string; label: string }> = {
  pending: { color: 'default', label: '排队中' },
  running: { color: 'processing', label: '执行中' },
  completed: { color: 'green', label: '已完成' },
  failed: { color: 'red', label: '失败' },
  expired: { color: 'default', label: '已过期' },
};

export default function ExportsPage() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<ExportTask[]>([]);
  const [allowed, setAllowed] = useState<string[]>([]);
  const [maxRows, setMaxRows] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [creating, setCreating] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();

  // Listing needs finance.read while creating and downloading need the separate
  // export.create, so the two calls are settled independently: a role that may
  // read the history but not egress the data still gets its list.
  const canCreate = (() => {
    try {
      return !!JSON.parse(localStorage.getItem('cp_admin') || 'null')?.permissions?.includes('export.create');
    } catch {
      return false;
    }
  })();

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    const [tasks, resources] = await Promise.allSettled([
      apiGet<{ items: ExportTask[] }>(`${endpoint}?page=1&page_size=50`),
      apiGet<{ items: string[]; max_rows: number }>(`${endpoint}/resources`),
    ]);
    if (tasks.status === 'fulfilled') setRows(tasks.value.items || []);
    else setError(tasks.reason?.message || '导出记录读取失败');
    if (resources.status === 'fulfilled') {
      setAllowed(resources.value.items || []);
      setMaxRows(resources.value.max_rows || 0);
    } else {
      setAllowed([]);
    }
    setLoading(false);
  }, []);

  useEffect(() => { void load(); }, [load]);

  const create = async () => {
    const values = await form.validateFields();
    setSaving(true);
    try {
      const result = await apiPost<{ status: string; row_count: number }>(endpoint, {
        request_id: crypto.randomUUID(), resource: values.resource, filter: {},
      });
      if (result?.status === 'completed') message.success(`导出完成，共 ${result.row_count} 行`);
      else message.warning('导出未完成，请刷新查看详情');
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '导出失败');
    } finally {
      setSaving(false);
    }
  };

  const download = async (row: ExportTask) => {
    try {
      // Fetch through the authenticated client so the bearer token is sent, then
      // hand the browser a blob URL to save.
      const response = await fetch(`${endpoint}/${row.id}/download`, {
        headers: { Authorization: `Bearer ${localStorage.getItem('cp_token') || ''}` },
      });
      if (!response.ok) throw new Error(response.status === 410 ? '文件已过期，请重新导出' : `下载失败（${response.status}）`);
      const blob = await response.blob();
      const url = URL.createObjectURL(blob);
      const link = document.createElement('a');
      link.href = url;
      link.download = `${row.task_no}.csv`;
      document.body.appendChild(link);
      link.click();
      link.remove();
      // Revoking synchronously races the browser's download start-up and can
      // silently cancel the save; give it a tick to pick the blob up first.
      setTimeout(() => URL.revokeObjectURL(url), 1000);
    } catch (e: any) {
      message.error(e?.message || '下载失败');
    }
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }} wrap>
        <Title level={3} style={{ margin: 0 }}>数据导出</Title>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        <Button type="primary" disabled={!canCreate || allowed.length === 0} onClick={() => setCreating(true)}>创建导出</Button>
        {error && <Text type="danger">{error}</Text>}
      </Space>
      {!canCreate ? (
        <Alert type="info" showIcon message="当前账号可查看导出记录，但无导出权限" description="创建与下载需要 export.create，导出属于批量数据出域操作，仅授予客户管理员。请联系客户管理员开通。" />
      ) : allowed.length === 0 ? (
        <Alert type="warning" showIcon message="当前账号没有任何导出权限" description="请联系客户管理员授予 export.create 及对应资源权限。" />
      ) : (
        <Paragraph type="secondary">导出文件 24 小时后过期，下载时会再次校验权限。</Paragraph>
      )}
      <Table<ExportTask>
        rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 900 }} pagination={false}
        columns={[
          { title: '任务号', dataIndex: 'task_no', width: 220 },
          { title: '资源', dataIndex: 'resource', width: 120, render: (v: string) => resourceLabel[v] || v },
          { title: '行数', dataIndex: 'row_count', width: 90 },
          { title: '状态', dataIndex: 'status', width: 100, render: (v: string) => <Tag color={statusMeta[v]?.color}>{statusMeta[v]?.label || v}</Tag> },
          { title: '错误', dataIndex: 'error_msg', ellipsis: true, render: (v: string | null) => v || '—' },
          { title: '过期时间', dataIndex: 'expires_at', width: 180, render: (v: string | null) => v || '—' },
          {
            title: '操作', width: 120, render: (_, row) => (
              row.status === 'completed' && canCreate
                ? <Button type="link" icon={<DownloadOutlined />} onClick={() => void download(row)}>下载</Button>
                : <Text type="secondary">—</Text>
            ),
          },
        ]}
      />
      <ModalLazy open={creating} onCancel={() => setCreating(false)} onOk={() => void create()}
        saving={saving} form={form} options={allowed} maxRows={maxRows} />
    </div>
  );
}

function ModalLazy(props: {
  open: boolean;
  onCancel: () => void;
  onOk: () => void;
  saving: boolean;
  form: ReturnType<typeof Form.useForm>[0];
  options: string[];
  maxRows: number;
}) {
  return (
    <Modal title="创建导出任务" open={props.open} onCancel={props.onCancel} onOk={props.onOk} confirmLoading={props.saving} okText="开始导出" cancelText="取消" destroyOnClose>
      <Form form={props.form} layout="vertical" preserve={false}>
        <Form.Item name="resource" label="导出资源" rules={[{ required: true, message: '请选择资源' }]}>
          <Select options={props.options.map((v) => ({ value: v, label: resourceLabel[v] || v }))} />
        </Form.Item>
      </Form>
      <Paragraph type="secondary">单次最多导出 {props.maxRows || 50000} 行。</Paragraph>
    </Modal>
  );
}
