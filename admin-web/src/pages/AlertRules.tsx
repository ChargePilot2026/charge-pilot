import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Alert, App, Button, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Table, Tag, Typography } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiDelete, apiGet, apiPost, apiPut } from '../api/client';
import { LoadError } from '../components/LoadError';
import { TABLE_PAGINATION, useTablePagination } from '../utils/tablePagination';

const { Text, Paragraph } = Typography;
const endpoint = '/api/v1/admin/alert-rules';
const subscriptionEndpoint = '/api/v1/admin/alert-subscriptions';

interface AlertRule {
  id: number;
  name: string;
  device_id_pattern: string;
  metric: string;
  op: string;
  threshold: number | [number, number];
  window_seconds: number;
  severity: string;
  enabled: boolean;
  open_alerts: number;
}

interface AlertSubscription {
  id: number;
  rule_id: number | null;
  severity: string | null;
  webhook_subscription_id: number | null;
  admin_user_id: number | null;
  enabled: boolean;
}

const metricLabel: Record<string, string> = {
  voltage_v: '电压 (V)', current_a: '电流 (A)', temperature_c: '温度 (°C)',
  battery_soc: '电池电量 (%)', power_w: '功率 (W)', meter_kwh: '累计电量 (kWh)',
};
const operatorLabel: Record<string, string> = {
  '>': '大于', '<': '小于', '>=': '大于等于', '<=': '小于等于', '==': '等于', '!=': '不等于', between: '介于',
};
const severityMeta: Record<string, { color: string; label: string }> = {
  warning: { color: 'gold', label: '警告' },
  critical: { color: 'orange', label: '严重' },
  fatal: { color: 'red', label: '致命' },
};

const thresholdText = (value: AlertRule['threshold']) => {
  if (Array.isArray(value)) return `${value[0]} ~ ${value[1]}`;
  return String(value);
};

/** AlertRules 管理 worker 每一轮都会求值的遥测规则。 */
export function AlertRules() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<AlertRule[]>([]);
  const [total, setTotal] = useState(0);
  const { pagination, tablePagination } = useTablePagination();
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [editing, setEditing] = useState<AlertRule | null>(null);
  const [creating, setCreating] = useState(false);
  const [saving, setSaving] = useState(false);
  const [resolving, setResolving] = useState<AlertRule | null>(null);
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await apiGet<{ items: AlertRule[]; total: number }>(`${endpoint}?page=${pagination.page}&page_size=${pagination.page_size}`);
      setRows(data.items || []);
      setTotal(data.total || 0);
    } catch (e: any) {
      setError(e?.message || '告警规则读取失败');
    } finally {
      setLoading(false);
    }
  }, [pagination.page, pagination.page_size]);

  useEffect(() => { void load(); }, [load]);

  const openEditor = (rule: AlertRule | null) => {
    setEditing(rule);
    setCreating(!rule);
    form.resetFields();
    if (rule) {
      form.setFieldsValue({
        name: rule.name, device_id_pattern: rule.device_id_pattern, metric: rule.metric, op: rule.op,
        window_seconds: rule.window_seconds, severity: rule.severity, enabled: rule.enabled,
        threshold_low: Array.isArray(rule.threshold) ? rule.threshold[0] : rule.threshold,
        threshold_high: Array.isArray(rule.threshold) ? rule.threshold[1] : undefined,
      });
    } else {
      form.setFieldsValue({ device_id_pattern: '*', severity: 'warning', window_seconds: 60, op: '>', enabled: true });
    }
  };

  const submit = async () => {
    const values = await form.validateFields();
    const threshold = values.op === 'between'
      ? [Number(values.threshold_low), Number(values.threshold_high)]
      : Number(values.threshold_low);
    const payload = {
      name: values.name, device_id_pattern: values.device_id_pattern || '*', metric: values.metric,
      op: values.op, threshold, window_seconds: Number(values.window_seconds) || 60,
      severity: values.severity, enabled: !!values.enabled,
    };
    setSaving(true);
    try {
      if (editing) await apiPut(`${endpoint}/${editing.id}`, payload);
      else await apiPost(endpoint, payload);
      message.success(editing ? '规则已更新' : '规则已创建');
      setEditing(null);
      setCreating(false);
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const remove = async (rule: AlertRule) => {
    setSaving(true);
    try {
      await apiDelete(`${endpoint}/${rule.id}`);
      message.success('规则已删除');
      await load();
    } catch (e: any) {
      message.error(e?.message || '删除失败');
    } finally {
      setSaving(false);
    }
  };

  const resolveAll = async (rule: AlertRule) => {
    setSaving(true);
    try {
      await apiPost(`${endpoint}/${rule.id}/resolve`, { note: '运维人工关闭' });
      message.success('该规则的未处理告警已全部关闭');
      setResolving(null);
      await load();
    } catch (e: any) {
      message.error(e?.message || '关闭失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <Paragraph type="secondary">规则由 worker 依据 gateway 遥测实时评估；指标恢复正常后告警会自动恢复。</Paragraph>
      <Space style={{ marginBottom: 12 }} wrap>
        <Button type="primary" icon={<PlusOutlined />} onClick={() => openEditor(null)}>新建规则</Button>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        </Space>
      {error && <LoadError title="告警规则加载失败" detail={error} onRetry={() => void load()} />}
      {total === 0 && !loading ? (
        <Alert type="info" showIcon message="尚未配置告警规则" description="新建规则后，超出阈值的遥测会自动生成告警并推送给订阅方。" />
      ) : (
        <Table<AlertRule> size="middle"
          rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 1000 }}
          pagination={{ ...tablePagination, total }}
          columns={[
            { title: '名称', dataIndex: 'name', width: 180 },
            { title: '设备匹配', dataIndex: 'device_id_pattern', width: 150, render: (v: string) => <Text code>{v}</Text> },
            { title: '指标', dataIndex: 'metric', width: 160, render: (v: string) => metricLabel[v] || v },
            {
              title: '触发条件', width: 200, render: (_, row) => (
                <>{operatorLabel[row.op]} {thresholdText(row.threshold)}
                  {row.window_seconds !== 60 && <Text type="secondary">（{row.window_seconds}s 窗口）</Text>}
                </>
              ),
            },
            { title: '级别', dataIndex: 'severity', width: 90, render: (v: string) => <Tag color={severityMeta[v]?.color}>{severityMeta[v]?.label || v}</Tag> },
            { title: '状态', dataIndex: 'enabled', width: 90, render: (v: boolean) => v ? <Tag color="green">启用</Tag> : <Tag>停用</Tag> },
            { title: '未处理告警', dataIndex: 'open_alerts', width: 110, render: (v: number) => (v > 0 ? <Tag color="red">{v}</Tag> : 0) },
            {
              title: '操作', fixed: 'right', width: 220, render: (_, row) => (<Space>
                <Button type="link" onClick={() => openEditor(row)}>编辑</Button>
                <Button type="link" disabled={row.open_alerts === 0} onClick={() => setResolving(row)}>关闭告警</Button>
                <Popconfirm title="删除该规则？" description="已产生的告警记录会保留。" onConfirm={() => void remove(row)}>
                  <Button type="link" danger>删除</Button>
                </Popconfirm>
              </Space>),
            },
          ]}
        />
      )}
      <Modal
        title={editing ? `编辑规则 ${editing.name}` : '新建告警规则'} open={creating || !!editing}
        onCancel={() => { setCreating(false); setEditing(null); }} onOk={() => void submit()}
        confirmLoading={saving} okText="保存" cancelText="取消" width={560} destroyOnHidden
      >
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="规则名称" rules={[{ required: true, message: '请填写规则名称' }, { max: 128 }]}>
            <Input maxLength={128} placeholder="例如：充电桩过温保护" />
          </Form.Item>
          <Form.Item name="device_id_pattern" label="设备匹配" tooltip="支持 * 通配符，* 匹配全部设备">
            <Input maxLength={128} placeholder="*" />
          </Form.Item>
          <Form.Item name="metric" label="监控指标" rules={[{ required: true, message: '请选择指标' }]}>
            <Select options={Object.entries(metricLabel).map(([value, label]) => ({ value, label }))} />
          </Form.Item>
          <Form.Item name="op" label="比较方式" rules={[{ required: true }]}>
            <Select options={Object.entries(operatorLabel).map(([value, label]) => ({ value, label }))} />
          </Form.Item>
          <Form.Item noStyle shouldUpdate={(prev, next) => prev.op !== next.op}>
            {({ getFieldValue }) => (
              <>
                {getFieldValue('op') === 'between' ? (
                  <Space>
                    <Form.Item name="threshold_low" label="下限" rules={[{ required: true, message: '请填写下限' }]}>
                      <InputNumber style={{ width: 140 }} />
                    </Form.Item>
                    <Form.Item name="threshold_high" label="上限" rules={[{ required: true, message: '请填写上限' }]}>
                      <InputNumber style={{ width: 140 }} />
                    </Form.Item>
                  </Space>
                ) : (
                  <Form.Item name="threshold_low" label="阈值" rules={[{ required: true, message: '请填写阈值' }]}>
                    <InputNumber style={{ width: 200 }} />
                  </Form.Item>
                )}
              </>
            )}
          </Form.Item>
          <Form.Item name="window_seconds" label="评估窗口（秒）">
            <InputNumber min={1} max={86400} style={{ width: 200 }} />
          </Form.Item>
          <Form.Item name="severity" label="严重级别" rules={[{ required: true }]}>
            <Select options={Object.entries(severityMeta).map(([value, meta]) => ({ value, label: meta.label }))} />
          </Form.Item>
          <Form.Item name="enabled" label="启用" valuePropName="checked"><Select options={[{ value: true, label: '启用' }, { value: false, label: '停用' }]} /></Form.Item>
        </Form>
      </Modal>
      <Modal title={`关闭「${resolving?.name || ''}」的全部告警`} open={!!resolving} onCancel={() => setResolving(null)}
        onOk={() => void resolveAll(resolving!)} confirmLoading={saving} okText="确认关闭" cancelText="取消">
        <Paragraph>将关闭该规则下 {resolving?.open_alerts || 0} 条未处理告警。此操作会写入审计日志。</Paragraph>
      </Modal>
    </div>
  );
}

/** AlertSubscriptions 把告警路由到 webhook 或指定的人。 */
export function AlertSubscriptions() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<AlertSubscription[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await apiGet<{ items: AlertSubscription[] }>(subscriptionEndpoint);
      setRows(data.items || []);
    } catch (e: any) {
      setError(e?.message || '订阅读取失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const submit = async () => {
    const values = await form.validateFields();
    setSaving(true);
    try {
      await apiPost(subscriptionEndpoint, {
        rule_id: values.rule_id || null,
        severity: values.severity || null,
        webhook_subscription_id: values.webhook_subscription_id || null,
        admin_user_id: values.admin_user_id || null,
        enabled: true,
      });
      message.success('订阅已创建');
      setOpen(false);
      form.resetFields();
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '创建失败');
    } finally {
      setSaving(false);
    }
  };

  const remove = async (row: AlertSubscription) => {
    setSaving(true);
    try {
      await apiDelete(`${subscriptionEndpoint}/${row.id}`);
      message.success('订阅已删除');
      await load();
    } catch (e: any) {
      message.error(e?.message || '删除失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <Space style={{ marginBottom: 12 }} wrap>
        <Button type="primary" onClick={() => setOpen(true)}>新建订阅</Button>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        </Space>
      {error && <LoadError title="告警订阅加载失败" detail={error} onRetry={() => void load()} />}
      {rows.length === 0 && !loading ? (
        <Alert type="info" showIcon message="尚未配置告警订阅" description="订阅后，对应告警会推送到指定的 Webhook 或运营账号。" />
      ) : (
        <Table<AlertSubscription> size="middle"
          rowKey="id" loading={loading} dataSource={rows} pagination={TABLE_PAGINATION}
          columns={[
            { title: 'ID', dataIndex: 'id', width: 80 },
            { title: '规则 ID', dataIndex: 'rule_id', width: 110, render: (v: number | null) => v ?? '全部规则' },
            { title: '严重级别', dataIndex: 'severity', width: 120, render: (v: string | null) => v ? <Tag color={severityMeta[v]?.color}>{severityMeta[v]?.label || v}</Tag> : '全部级别' },
            { title: 'Webhook 订阅', dataIndex: 'webhook_subscription_id', width: 150, render: (v: number | null) => v ? `#${v}` : '—' },
            { title: '接收账号', dataIndex: 'admin_user_id', width: 130, render: (v: number | null) => v ? `#${v}` : '—' },
            { title: '状态', dataIndex: 'enabled', width: 90, render: (v: boolean) => v ? <Tag color="green">启用</Tag> : <Tag>停用</Tag> },
            { title: '操作', width: 100, render: (_, row) => <Button type="link" danger onClick={() => void remove(row)}>删除</Button> },
          ]}
        />
      )}
      <Modal title="新建告警订阅" open={open} onCancel={() => setOpen(false)} onOk={() => void submit()}
        confirmLoading={saving} okText="创建" cancelText="取消" destroyOnHidden>
        <Paragraph type="secondary">规则和严重级别至少填一项；Webhook 与接收账号至少填一项。</Paragraph>
        <Form form={form} layout="vertical">
          <Form.Item name="rule_id" label="规则 ID（留空表示全部规则）"><InputNumber style={{ width: '100%' }} min={1} /></Form.Item>
          <Form.Item name="severity" label="严重级别（留空表示全部级别）">
            <Select allowClear options={Object.entries(severityMeta).map(([value, meta]) => ({ value, label: meta.label }))} />
          </Form.Item>
          <Form.Item name="webhook_subscription_id" label="Webhook 订阅 ID"><InputNumber style={{ width: '100%' }} min={1} /></Form.Item>
          <Form.Item name="admin_user_id" label="接收账号 ID"><InputNumber style={{ width: '100%' }} min={1} /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
}

export default function AlertRulesPage() {
  return (
    <div className="page-container">
      <TabsLazy sections={[
        { key: 'rules', label: '告警规则', node: <AlertRules /> },
        { key: 'subscriptions', label: '告警订阅', node: <AlertSubscriptions /> },
      ]} />
    </div>
  );
}

function TabsLazy({ sections }: { sections: { key: string; label: string; node: ReactNode }[] }) {
  const [active, setActive] = useState(sections[0].key);
  const current = sections.find((s) => s.key === active) || sections[0];
  return (
    <div>
      <Space style={{ marginBottom: 12 }}>
        {sections.map((s) => (
          <Button key={s.key} type={active === s.key ? 'primary' : 'default'} onClick={() => setActive(s.key)}>{s.label}</Button>
        ))}
      </Space>
      {current.node}
    </div>
  );
}
