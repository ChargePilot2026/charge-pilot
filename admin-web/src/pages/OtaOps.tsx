import { useCallback, useEffect, useState } from 'react';
import { Alert, App, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd';
import { PlusOutlined, ReloadOutlined, ThunderboltOutlined } from '@ant-design/icons';
import { apiDelete, apiGet, apiPost } from '../api/client';

const { Text, Paragraph } = Typography;
const packagesEndpoint = '/api/v1/admin/ota/packages';
const schedulesEndpoint = '/api/v1/admin/ota/schedules';

interface OtaPackage {
  id: number;
  code: string;
  vendor_id: number | null;
  version: string;
  storage_url: string;
  size_bytes: number;
  checksum_sha256: string;
  release_notes: string | null;
  status: string;
  created_at: string;
}

interface OtaSchedule {
  id: number;
  package_id: number;
  target_filter: unknown;
  rollout_strategy: string;
  batch_size: number | null;
  status: string;
  scheduled_at: string | null;
  started_at: string | null;
  completed_at: string | null;
  progress: { total?: number; acked?: number; failed?: number; pending?: number; available?: boolean };
}

const packageStatus: Record<string, { color: string; label: string }> = {
  draft: { color: 'default', label: '草稿' },
  published: { color: 'green', label: '已发布' },
  archived: { color: 'red', label: '已归档' },
};
const scheduleStatus: Record<string, { color: string; label: string }> = {
  pending: { color: 'default', label: '待执行' },
  running: { color: 'processing', label: '进行中' },
  completed: { color: 'green', label: '已完成' },
  cancelled: { color: 'default', label: '已取消' },
  failed: { color: 'red', label: '失败' },
};
const strategyLabel: Record<string, string> = { all: '全量', canary: '灰度', batch: '分批', manual: '手动' };
const sizeText = (bytes: number) => bytes > 1 << 20 ? `${(bytes / (1 << 20)).toFixed(1)} MB` : `${Math.round(bytes / 1024)} KB`;

/** OtaPackages records firmware metadata; the artifact itself lives in HTTPS storage. */
export function OtaPackages() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<OtaPackage[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await apiGet<{ items: OtaPackage[] }>(`${packagesEndpoint}?page=1&page_size=100`);
      setRows(data.items || []);
    } catch (e: any) {
      setError(e?.message || '固件包读取失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const submit = async () => {
    const values = await form.validateFields();
    setSaving(true);
    try {
      await apiPost(packagesEndpoint, {
        code: values.code, version: values.version, storage_url: values.storage_url,
        size_bytes: Number(values.size_bytes), checksum_sha256: values.checksum_sha256,
        release_notes: values.release_notes || null, publish: !!values.publish,
      });
      message.success(values.publish ? '固件包已发布' : '固件包已保存为草稿');
      setOpen(false);
      form.resetFields();
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const remove = async (row: OtaPackage) => {
    setSaving(true);
    try {
      await apiDelete(`${packagesEndpoint}/${row.id}`);
      message.success('固件包已归档');
      await load();
    } catch (e: any) {
      message.error(e?.message || '删除失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <Paragraph type="secondary">固件需先上传到 HTTPS 存储，再在此登记校验和；只有已发布的包可以创建升级计划。</Paragraph>
      <Space style={{ marginBottom: 12 }} wrap>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>登记固件</Button>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        {error && <Text type="danger">{error}</Text>}
      </Space>
      {rows.length === 0 && !loading ? (
        <Alert type="info" showIcon message="暂无固件包" description="登记并发布固件后即可创建 OTA 升级计划。" />
      ) : (
        <Table<OtaPackage>
          rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 1000 }}
          columns={[
            { title: '编码', dataIndex: 'code', width: 150 },
            { title: '版本', dataIndex: 'version', width: 110 },
            { title: '大小', dataIndex: 'size_bytes', width: 100, render: sizeText },
            { title: 'SHA256', dataIndex: 'checksum_sha256', width: 130, render: (v: string) => <Text code copyable>{v.slice(0, 10)}…</Text> },
            { title: '下载地址', dataIndex: 'storage_url', ellipsis: true },
            { title: '状态', dataIndex: 'status', width: 100, render: (v: string) => <Tag color={packageStatus[v]?.color}>{packageStatus[v]?.label || v}</Tag> },
            { title: '操作', width: 90, render: (_, row) => (
              <Popconfirm title="归档该固件包？" description="进行中的升级计划不会被中断。" onConfirm={() => void remove(row)}>
                <Button type="link" danger>归档</Button>
              </Popconfirm>
            ) },
          ]}
        />
      )}
      <Modal title="登记固件包" open={open} onCancel={() => setOpen(false)} onOk={() => void submit()}
        confirmLoading={saving} okText="保存" cancelText="取消" width={560} destroyOnClose>
        <Form form={form} layout="vertical" initialValues={{ publish: true }}>
          <Form.Item name="code" label="固件编码" rules={[{ required: true, message: '请填写编码' }, { max: 64 }]}>
            <Input maxLength={64} placeholder="例如 DC589-CONTROLLER" />
          </Form.Item>
          <Form.Item name="version" label="版本号" rules={[{ required: true, message: '请填写版本号' }, { max: 64 }]}>
            <Input maxLength={64} placeholder="例如 1.4.2" />
          </Form.Item>
          <Form.Item name="storage_url" label="HTTPS 下载地址" rules={[{ required: true, message: '请填写下载地址' }, { type: 'url' }]}>
            <Input placeholder="https://..." />
          </Form.Item>
          <Form.Item name="size_bytes" label="文件大小（字节）" rules={[{ required: true, message: '请填写大小' }]}>
            <InputNumber style={{ width: '100%' }} min={1} />
          </Form.Item>
          <Form.Item name="checksum_sha256" label="SHA256 校验和" rules={[{ required: true, message: '请填写校验和' }, { len: 64, message: 'SHA256 为 64 位十六进制' }]}>
            <Input maxLength={64} placeholder="64 位十六进制" />
          </Form.Item>
          <Form.Item name="release_notes" label="更新说明"><Input.TextArea rows={3} maxLength={2000} showCount /></Form.Item>
          <Form.Item name="publish" label="立即发布" valuePropName="checked">
            <Select options={[{ value: true, label: '发布（可用于升级计划）' }, { value: false, label: '保存为草稿' }]} />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}

/** OtaSchedules creates rollout plans and triggers the device push. */
export function OtaSchedules() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<OtaSchedule[]>([]);
  const [packages, setPackages] = useState<OtaPackage[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const [schedules, firmware] = await Promise.all([
        apiGet<{ items: OtaSchedule[] }>(`${schedulesEndpoint}?page=1&page_size=100`),
        apiGet<{ items: OtaPackage[] }>(`${packagesEndpoint}?page=1&page_size=100&status=published`),
      ]);
      setRows(schedules.items || []);
      setPackages(firmware.items || []);
    } catch (e: any) {
      setError(e?.message || '升级计划读取失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const submit = async () => {
    const values = await form.validateFields();
    setSaving(true);
    try {
      await apiPost(schedulesEndpoint, {
        package_id: values.package_id,
        rollout_strategy: values.rollout_strategy,
        batch_size: values.batch_size || null,
        target_filter: values.station_ids
          ? { station_ids: String(values.station_ids).split(',').map((v: string) => Number(v.trim())).filter((v: number) => Number.isFinite(v) && v > 0) }
          : {},
      });
      message.success('升级计划已创建');
      setOpen(false);
      form.resetFields();
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '创建失败');
    } finally {
      setSaving(false);
    }
  };

  const trigger = async (row: OtaSchedule) => {
    setSaving(true);
    try {
      const result = await apiPost<{ dispatched: number }>(`${schedulesEndpoint}/${row.id}/trigger`, {});
      message.success(`已下发 ${result?.dispatched ?? 0} 台设备`);
      await load();
    } catch (e: any) {
      message.error(e?.message || '触发失败');
    } finally {
      setSaving(false);
    }
  };

  const cancel = async (row: OtaSchedule) => {
    setSaving(true);
    try {
      await apiPost(`${schedulesEndpoint}/${row.id}/cancel`, {});
      message.success('计划已取消');
      await load();
    } catch (e: any) {
      message.error(e?.message || '取消失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <Paragraph type="secondary">触发后按批次向 gateway 下发升级指令；灰度策略限制单次下发数量，可反复触发推进。</Paragraph>
      <Space style={{ marginBottom: 12 }} wrap>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => setOpen(true)}>新建计划</Button>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        {error && <Text type="danger">{error}</Text>}
      </Space>
      {rows.length === 0 && !loading ? (
        <Alert type="info" showIcon message="暂无升级计划" description="选择一个已发布的固件包即可创建计划。" />
      ) : (
        <Table<OtaSchedule>
          rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 1000 }}
          columns={[
            { title: 'ID', dataIndex: 'id', width: 70 },
            { title: '固件', dataIndex: 'package_id', width: 90, render: (v: number) => {
              const found = packages.find((p) => p.id === v);
              return found ? `${found.code} ${found.version}` : `#${v}`;
            } },
            { title: '策略', dataIndex: 'rollout_strategy', width: 100, render: (v: string) => strategyLabel[v] || v },
            { title: '批次', dataIndex: 'batch_size', width: 80, render: (v: number | null) => v ?? '—' },
            { title: '进度', width: 240, render: (_, row) => {
              const p = row.progress || {};
              if (!p.available) return <Text type="secondary">待下发</Text>;
              return <Text>{p.acked ?? 0}/{p.total ?? 0} 已确认{p.failed ? <Text type="danger">，{p.failed} 失败</Text> : null}</Text>;
            } },
            { title: '状态', dataIndex: 'status', width: 100, render: (v: string) => <Tag color={scheduleStatus[v]?.color}>{scheduleStatus[v]?.label || v}</Tag> },
            {
              title: '操作', fixed: 'right', width: 150, render: (_, row) => (
                ['pending', 'running'].includes(row.status) ? (
                  <Space>
                    <Button type="link" icon={<ThunderboltOutlined />} onClick={() => void trigger(row)} loading={saving}>触发</Button>
                    <Button type="link" danger onClick={() => void cancel(row)}>取消</Button>
                  </Space>
                ) : <Text type="secondary">—</Text>
              ),
            },
          ]}
        />
      )}
      <Modal title="新建升级计划" open={open} onCancel={() => setOpen(false)} onOk={() => void submit()}
        confirmLoading={saving} okText="创建" cancelText="取消" destroyOnClose>
        <Form form={form} layout="vertical" initialValues={{ rollout_strategy: 'canary', batch_size: 5 }}>
          <Form.Item name="package_id" label="固件包" rules={[{ required: true, message: '请选择固件包' }]}>
            <Select options={packages.map((p) => ({ value: p.id, label: `${p.code} ${p.version}` }))} />
          </Form.Item>
          <Form.Item name="rollout_strategy" label="发布策略" rules={[{ required: true }]}>
            <Select options={Object.entries(strategyLabel).map(([value, label]) => ({ value, label }))} />
          </Form.Item>
          <Form.Item name="batch_size" label="单批设备数（灰度必填）">
            <InputNumber style={{ width: '100%' }} min={1} max={10000} />
          </Form.Item>
          <Form.Item name="station_ids" label="限定站点 ID（留空表示全部站点，多个用英文逗号分隔）">
            <Input placeholder="例如 1,2,3" />
          </Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
