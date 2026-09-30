import { useEffect, useRef, useState } from 'react';
import { Alert, App, Button, Descriptions, Divider, Form, InputNumber, Modal, Select, Space, Spin, Switch, Table, Tabs, Tag } from 'antd';
import { apiGet, apiPut } from '../../api/client';
import { fromCents, toCents } from '../pricing/model';
import { LoadError } from '../../components/LoadError';

// 在站点工作区内管理启动与退款策略，并查看本站点的计费下发记录。
type StationPolicy = {
  station_id: number; station_name?: string; force_recharge: boolean; min_balance_cents: number;
  scan_refund_path: RefundPath; scan_refund_rule: ScanRefundRule;
  card_refund_path: RefundPath; card_refund_rule: CardRefundRule;
  timeout_start_refund: boolean; verify_phone_before_charge: boolean;
  version: number;
};
type SwitchTask = {
  id: number; task_no: string; station_id: number; station_name?: string; template_id: number;
  mode_before?: string; mode_after: string; device_count: number; status: string;
  requested_by: number; created_at: string; completed_at?: string;
};
type SwitchItem = {
  id: number; device_id: string; mode_before?: string; mode_after: string; status: string;
  offered_snapshot?: string; error_msg?: string;
};

// 退款是两个正交的问题，并且按支付方式分开提问：启动失败到底退不退、按哪条规则退、
// 以及钱退到哪里。后端存的正是这两项，所以编辑器也只问这两项，展示时再把这一对
// 折回一句话。
type RefundPath = 'balance' | 'original';
type ScanRefundRule = 'none' | 'realtime' | 'time_limited';
type CardRefundRule = 'none' | 'realtime' | 'time_limited_prorated';

const REFUND_PATH: { value: RefundPath; label: string; detail: string }[] = [
  { value: 'balance', label: '退回账户余额', detail: '退到用户账户余额，可继续用于下单' },
  { value: 'original', label: '原路退回', detail: '退到用户原支付渠道' },
];
const SCAN_RULE: { value: ScanRefundRule; label: string; detail: string }[] = [
  { value: 'none', label: '不退款', detail: '扫码启动失败不退还任何金额' },
  { value: 'realtime', label: '实时退款', detail: '扫码启动失败即退款，无时效限制' },
  { value: 'time_limited', label: '限时退款', detail: '扫码启动失败在时效内退款，时效外不退' },
];
const CARD_RULE: { value: CardRefundRule; label: string; detail: string }[] = [
  { value: 'none', label: '不退款', detail: '刷卡启动失败不退还任何金额' },
  { value: 'realtime', label: '实时退款', detail: '刷卡启动失败即按全额退款，无时效限制' },
  { value: 'time_limited_prorated', label: '限时按比例退款', detail: '刷卡启动失败在时效内按未使用时长比例退款，时效外不退' },
];
const pathLabel = (v?: string) => REFUND_PATH.find(o => o.value === v)?.label || v || '—';
const ruleDetail = (options: { value: string; detail: string }[], v?: string) => options.find(o => o.value === v)?.detail || '—';

// refundSentence 把存下来的两个字段读回运营会脱口而出的那一句话。规则为 "none" 时
// 没有退到哪儿，所以单独成句；限时规则必须把时效说明带上，否则这句话承诺过头了。
function refundSentence(rule: string | undefined, path: string | undefined) {
  const all: { value: string; label: string }[] = [...SCAN_RULE, ...CARD_RULE];
  const label = all.find(o => o.value === rule)?.label || rule || '—';
  if (rule === 'none' || !rule) return label;
  const caveat = rule === 'time_limited' || rule === 'time_limited_prorated' ? '（时效外不退款）' : '';
  return `${label}${caveat}-${pathLabel(path)}`;
}

const TASK_STATUS: Record<string, { label: string; color: string }> = {
  pending: { label: '待下发', color: 'default' }, running: { label: '进行中', color: 'processing' },
  succeeded: { label: '已成功', color: 'green' }, partial: { label: '部分失败', color: 'orange' },
  failed: { label: '失败', color: 'red' },
};
const ITEM_STATUS: Record<string, { label: string; color: string }> = {
  pending: { label: '待下发', color: 'default' }, succeeded: { label: '成功', color: 'green' }, failed: { label: '失败', color: 'red' },
};

export default function StationOperations({ station }: {
  station: { id: number; name: string; status: string };
}) {
  const { message } = App.useApp();
  const stationId = station.id;
  const currentStation = useRef<number | null>(stationId);
  currentStation.current = stationId;
  const generation = useRef(0);
  const policyTarget = useRef<{ stationId: number; version: number } | null>(null);
  const taskGeneration = useRef(0);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [policies, setPolicies] = useState<StationPolicy[]>([]);
  const [tasks, setTasks] = useState<SwitchTask[]>([]);
  const [taskStatus, setTaskStatus] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [canManageDefault, setCanManageDefault] = useState(false);
  const [error, setError] = useState('');

  const [policyOpen, setPolicyOpen] = useState(false);
  const [policyError, setPolicyError] = useState('');
  const [detail, setDetail] = useState<{ task: SwitchTask; items: SwitchItem[] } | null>(null);
  const [taskTarget, setTaskTarget] = useState<SwitchTask | null>(null);
  const [taskError, setTaskError] = useState<string | null>(null);

  const [policyForm] = Form.useForm();
  const scanRule = Form.useWatch<ScanRefundRule>('scan_refund_rule', policyForm) || 'time_limited';
  const cardRule = Form.useWatch<CardRefundRule>('card_refund_rule', policyForm) || 'time_limited_prorated';

  const canUpdate = permissions.includes('pricing.rule.update');
  const canEditPolicy = canUpdate && canManageDefault && station.status === 'active';
  const policy = policies.find(p => p.station_id === stationId);

  const load = async () => {
    const current = ++generation.current;
    setLoading(true);
    setError('');
    setPolicies([]); setPermissions([]); setTasks([]); setCanManageDefault(false);
    try {
      const [list, rules] = await Promise.all([
        apiGet<SwitchTask[]>('/api/v1/admin/settings/switch-tasks', { station_id: stationId, status: taskStatus || undefined }),
        apiGet<{ items: StationPolicy[]; permissions: string[]; can_manage_default: boolean }>('/api/v1/admin/settings/station-policies', { station_id: stationId }),
      ]);
      if (current !== generation.current) return;
      if (!Array.isArray(list) || !Array.isArray(rules?.items) || !Array.isArray(rules.permissions) || typeof rules.can_manage_default !== 'boolean') throw new Error('站点策略与下发记录响应不完整，请重新加载');
      setTasks(list); setPolicies(rules.items.filter(p => p.station_id === stationId)); setPermissions(rules.permissions);
      setCanManageDefault(rules.can_manage_default);
    } catch (e: any) {
      if (current === generation.current) setError(e?.response?.data?.message || e.message || '站点策略与下发记录读取失败');
    } finally {
      if (current === generation.current) setLoading(false);
    }
  };

  useEffect(() => {
    currentStation.current = stationId;
    void load();
    return () => {
      generation.current++; taskGeneration.current++;
      if (currentStation.current === stationId) currentStation.current = null;
    };
  }, [stationId, taskStatus]);
  useEffect(() => {
    setPolicyOpen(false); setTaskTarget(null); setDetail(null); setTaskError(null);
    policyTarget.current = null;
  }, [stationId]);

  const openPolicy = () => {
    if (!canEditPolicy || loading || saving || error) return;
    policyTarget.current = { stationId, version: policy?.version || 0 };
    setPolicyError('');
    policyForm.resetFields();
    policyForm.setFieldsValue({
      force_recharge: !!policy?.force_recharge,
      min_balance_yuan: fromCents(policy?.min_balance_cents),
      scan_refund_rule: policy?.scan_refund_rule || 'time_limited',
      scan_refund_path: policy?.scan_refund_path || 'original',
      card_refund_rule: policy?.card_refund_rule || 'time_limited_prorated',
      card_refund_path: policy?.card_refund_path || 'original',
      timeout_start_refund: !!policy?.timeout_start_refund,
      verify_phone_before_charge: !!policy?.verify_phone_before_charge,
    });
    setPolicyOpen(true);
  };

  const savePolicy = async () => {
    const target = policyTarget.current;
    if (!target || !canEditPolicy || saving) return;
    let values: any;
    try {
      values = await policyForm.validateFields();
    } catch {
      return;
    }
    if (target.stationId !== currentStation.current) { setPolicyError('当前站点已变化，请重新打开策略编辑'); return; }
    setSaving(true);
    setPolicyError('');
    try {
      await apiPut(`/api/v1/admin/settings/station-policies/${target.stationId}`, {
        force_recharge: !!values.force_recharge,
        min_balance_cents: toCents(values.min_balance_yuan),
        scan_refund_rule: values.scan_refund_rule,
        scan_refund_path: values.scan_refund_path,
        card_refund_rule: values.card_refund_rule,
        card_refund_path: values.card_refund_path,
        timeout_start_refund: !!values.timeout_start_refund,
        verify_phone_before_charge: !!values.verify_phone_before_charge,
        expected_version: target.version,
      });
      if (target.stationId !== currentStation.current) return;
      message.success('站点策略已保存');
      setPolicyOpen(false);
      await load();
    } catch (e: any) {
      if (target.stationId === currentStation.current) setPolicyError(e.message || '保存失败');
    } finally {
      if (target.stationId === currentStation.current) setSaving(false);
    }
  };

  // 明细取不到时把弹窗留在屏上并显示 LoadError，运营可以原地重试；这里只读，
  // 不用担心误操作。
  const openTask = async (task: SwitchTask) => {
    const expectedStation = currentStation.current;
    const expectedGeneration = ++taskGeneration.current;
    setTaskTarget(task); setDetail(null); setTaskError(null);
    try {
      const result = await apiGet<{ task: SwitchTask; items: SwitchItem[] }>(`/api/v1/admin/settings/switch-tasks/${task.id}`);
      if (expectedStation === currentStation.current && expectedGeneration === taskGeneration.current) setDetail(result);
    } catch (e: any) {
      if (expectedStation === currentStation.current && expectedGeneration === taskGeneration.current) setTaskError(e?.message || '切换任务明细读取失败');
    }
  };

  return <div>
    <Space style={{ marginBottom: 12 }} wrap>
      <Button onClick={() => void load()} loading={loading}>刷新</Button>
    </Space>
    {error && <LoadError title="站点策略与下发记录加载失败" detail={error} onRetry={() => void load()} />}
    <Tabs items={[
      {
        key: 'policy', label: '站点策略',
        children: <>
          <Alert type="info" showIcon style={{ marginBottom: 12 }}
            message="站点策略决定的是钱怎么动：用户能不能直接开始、启动失败退不退、退到哪里。它不参与任何一次计费计算。" />
          <Space direction="vertical" style={{ width: '100%' }}>
            <Descriptions size="small" column={2} bordered items={[
              { key: 'version', label: '策略版本', children: policy ? `v${policy.version}` : '尚未配置（保存后创建）' },
              { key: 'recharge', label: '强制充值', children: policy?.force_recharge ? '是：余额不足时必须先充值' : '否：允许余额直接开单' },
              { key: 'balance', label: '最低余额', children: `¥${fromCents(policy?.min_balance_cents).toFixed(2)}` },
              { key: 'phone', label: '充电前验证手机号', children: policy?.verify_phone_before_charge ? '是' : '否' },
              {
                key: 'scan', label: '扫码启动失败', span: 2,
                children: policy ? refundSentence(policy.scan_refund_rule, policy.scan_refund_path) : '—',
              },
              {
                key: 'card', label: '刷卡启动失败', span: 2,
                children: policy ? refundSentence(policy.card_refund_rule, policy.card_refund_path) : '—',
              },
              { key: 'timeout', label: '启动超时处理', span: 2, children: policy?.timeout_start_refund ? '自动退款' : '不自动退款' },
            ]} />
            {canEditPolicy && <Button type="primary" onClick={openPolicy} disabled={loading || !!error || saving}>编辑站点策略</Button>}
          </Space>
        </>,
      },
      {
        key: 'tasks', label: '下发记录',
        children: <>
          <Space style={{ marginBottom: 12 }}>
            <Select aria-label="任务状态" style={{ width: 160 }} value={taskStatus} onChange={setTaskStatus}
              options={[{ value: '', label: '全部状态' }, ...Object.entries(TASK_STATUS).map(([value, s]) => ({ value, label: s.label }))]} />
            <span style={{ color: '#666' }}>仅显示最近 200 条</span>
          </Space>
          <Table<SwitchTask> rowKey="id" dataSource={tasks} loading={loading} scroll={{ x: 900 }}
            columns={[
              { title: '任务号', dataIndex: 'task_no' },
              { title: '站点', render: (_: unknown, r: SwitchTask) => r.station_name || `站点 #${r.station_id}` },
              { title: '计费方式', render: (_: unknown, r: SwitchTask) => `${r.mode_before || '未配置'} → ${r.mode_after}` },
              { title: '设备数', dataIndex: 'device_count' },
              { title: '状态', dataIndex: 'status', render: (s: string) => <Tag color={TASK_STATUS[s]?.color || 'default'}>{TASK_STATUS[s]?.label || s}</Tag> },
              { title: '创建时间', dataIndex: 'created_at', render: (v?: string) => (v ? new Date(v).toLocaleString() : '-') },
              { title: '操作', key: 'ops', render: (_: unknown, r: SwitchTask) => <Button type="link" onClick={() => void openTask(r)}>设备明细</Button> },
            ]} />
        </>,
      },
    ]} />

    <Modal title="编辑站点策略" open={policyOpen} width={720} onCancel={() => { if (!saving) setPolicyOpen(false); }}
      closable={!saving} maskClosable={!saving} keyboard={!saving} cancelButtonProps={{ disabled: saving }}
      onOk={() => void savePolicy()} confirmLoading={saving} okText="保存" destroyOnHidden>
      {policyError && <Alert type="error" showIcon message={policyError} style={{ marginBottom: 12 }} />}
      <Form form={policyForm} name="station_policy" layout="vertical">
        <Form.Item name="force_recharge" label="强制先充值" valuePropName="checked" extra="开启后余额不足时用户必须先充值才能开单。">
          <Switch />
        </Form.Item>
        <Form.Item name="min_balance_yuan" label="最低余额（元）" extra="开单时账户余额不得低于该金额，0 表示不校验。">
          <InputNumber min={0} max={10000} step={1} precision={2} addonBefore="¥" style={{ width: 200 }} />
        </Form.Item>
        <Divider orientation="left" plain>退费</Divider>
        <Form.Item label="扫码启动失败" extra={ruleDetail(SCAN_RULE, scanRule)}>
          <Space wrap>
            <Form.Item name="scan_refund_rule" noStyle rules={[{ required: true, message: '请选择扫码退费规则' }]}>
              <Select style={{ width: 220 }} options={SCAN_RULE.map(o => ({ value: o.value, label: o.label }))} />
            </Form.Item>
            <Form.Item name="scan_refund_path" noStyle rules={[{ required: true, message: '请选择扫码退款去向' }]}>
              <Select style={{ width: 220 }} options={REFUND_PATH.map(o => ({ value: o.value, label: o.label }))} />
            </Form.Item>
          </Space>
        </Form.Item>
        <Form.Item label="刷卡启动失败" extra={ruleDetail(CARD_RULE, cardRule)}>
          <Space wrap>
            <Form.Item name="card_refund_rule" noStyle rules={[{ required: true, message: '请选择刷卡退费规则' }]}>
              <Select style={{ width: 220 }} options={CARD_RULE.map(o => ({ value: o.value, label: o.label }))} />
            </Form.Item>
            <Form.Item name="card_refund_path" noStyle rules={[{ required: true, message: '请选择刷卡退款去向' }]}>
              <Select style={{ width: 220 }} options={REFUND_PATH.map(o => ({ value: o.value, label: o.label }))} />
            </Form.Item>
          </Space>
        </Form.Item>
        <Form.Item name="timeout_start_refund" label="启动超时自动退款" valuePropName="checked">
          <Switch />
        </Form.Item>
        <Form.Item name="verify_phone_before_charge" label="充电前必须验证手机号" valuePropName="checked">
          <Switch />
        </Form.Item>
      </Form>
    </Modal>

    <Modal title={detail ? `切换任务 ${detail.task.task_no}` : taskTarget ? `切换任务 ${taskTarget.task_no}` : '切换任务'} open={!!taskTarget} width={1000}
      onCancel={() => { taskGeneration.current++; setTaskTarget(null); setDetail(null); }}
      footer={<Button onClick={() => { taskGeneration.current++; setTaskTarget(null); setDetail(null); }}>关闭</Button>}>
      {taskError ? (
        <LoadError title="切换任务明细加载失败" detail={taskError}
          onRetry={() => { if (taskTarget) void openTask(taskTarget); }} />
      ) : !detail ? <Spin /> : <>
        <Descriptions size="small" column={2} bordered items={[
          { key: 'no', label: '任务号', children: detail.task.task_no },
          { key: 'status', label: '状态', children: <Tag color={TASK_STATUS[detail.task.status]?.color || 'default'}>{TASK_STATUS[detail.task.status]?.label || detail.task.status}</Tag> },
          { key: 'mode', label: '计费方式', children: `${detail.task.mode_before || '未配置'} → ${detail.task.mode_after}` },
          { key: 'count', label: '设备数', children: detail.task.device_count },
          { key: 'created', label: '创建时间', children: detail.task.created_at ? new Date(detail.task.created_at).toLocaleString() : '-' },
          { key: 'completed', label: '完成时间', children: detail.task.completed_at ? new Date(detail.task.completed_at).toLocaleString() : '未完成' },
        ]} />
        <Divider orientation="left" plain>设备明细</Divider>
        <Table<SwitchItem> rowKey="id" size="small" dataSource={detail.items} pagination={{ pageSize: 20 }}
          columns={[
            { title: '设备编号', dataIndex: 'device_id' },
            { title: '下发前', dataIndex: 'mode_before', render: (v?: string) => v || '未配置' },
            { title: '下发后', dataIndex: 'mode_after' },
            { title: '结果', dataIndex: 'status', render: (s: string) => <Tag color={ITEM_STATUS[s]?.color || 'default'}>{ITEM_STATUS[s]?.label || s}</Tag> },
            { title: '下发套餐快照', dataIndex: 'offered_snapshot', render: (v?: string) => v || '—' },
            { title: '失败原因', dataIndex: 'error_msg', render: (v?: string) => v || '—' },
          ]} />
      </>}
    </Modal>
  </div>;
}
