import { useEffect, useState } from 'react';
import { Alert, Button, Descriptions, Divider, Form, Input, InputNumber, Modal, Select, Space, Switch, Table, Tabs, Tag, Tooltip, message } from 'antd';
import { apiGet, apiPost, apiPut } from '../api/client';
import { fromCents, modeLabel, toCents, type Spec, type Station, type Template } from './pricing/model';
import { LoadError } from '../components/LoadError';

// 一个站点的全部：它收多少钱、每个桩在跑什么、以及上次那次切换到底有没有生效。
// 三个页签回答的是运营打开站点前要问的三个不同问题：充电用户能不能启动、每个桩实际
// 在收多少钱、以及我上周改的东西到了没有。
//
// 站点策略管的是钱往哪儿走，不是一次充电怎么计价，所以它独立成一个页签，而不是塞进
// 某个电价模板里。

type StationPolicy = {
  station_id: number; station_name?: string; force_recharge: boolean; min_balance_cents: number;
  scan_refund_path: RefundPath; scan_refund_rule: ScanRefundRule;
  card_refund_path: RefundPath; card_refund_rule: CardRefundRule;
  timeout_start_refund: boolean; verify_phone_before_charge: boolean;
  version: number;
};
type DeviceRow = {
  device_id: string; model?: string; device_status: string;
  template_name?: string; template_id?: number; own_rule_id?: number; own_version?: number; station_version?: number;
  // 生效后的 spec 由服务端解析，所以矩阵能直接回答「这个桩实际在收多少钱」，不必再发一轮请求。
  spec_json?: Spec;
  // 为 null 表示它继承的那一行是站点默认值，而不是这个桩自己的规则。
  effective_rule_device_id?: string | null;
  // 下发模板时乐观锁比对的版本号。这一对字段刻意不过滤 status，与服务端读法一致：
  // own_rule_id/own_version/station_version 描述的是「现在正在生效什么」，
  // latest 描述的是「这条链上最新到第几版」，后者才是回填 expected_version 该用的。
  own_latest_version?: number;
  offer_count?: number; offer_names?: string;
  // 板端自己能报什么，以记录在设备上的为准。这两个开关在人工声明之前都是 false，
  // 而没声明的板子根本无法按电表计价——所以这一列是为了让这件事在 apply 被拒绝之前就
  // 看得见，而不是被拒之后才发现。
  charge_mode?: string; reports_energy?: boolean; reports_segmented_power?: boolean;
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

export default function StationPricing() {
  const [stationId, setStationId] = useState<number | null>(null);
  const [stations, setStations] = useState<Station[]>([]);
  const [stationsLoading, setStationsLoading] = useState(false);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [policies, setPolicies] = useState<StationPolicy[]>([]);
  const [devices, setDevices] = useState<DeviceRow[]>([]);
  const [templates, setTemplates] = useState<Template[]>([]);
  const [tasks, setTasks] = useState<SwitchTask[]>([]);
  const [taskStatus, setTaskStatus] = useState('');
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');

  const [policyOpen, setPolicyOpen] = useState(false);
  const [policyError, setPolicyError] = useState('');
  const [assigning, setAssigning] = useState<DeviceRow | null>(null);
  const [assignTemplate, setAssignTemplate] = useState<number | null>(null);
  const [assignError, setAssignError] = useState('');
  const [detail, setDetail] = useState<{ task: SwitchTask; items: SwitchItem[] } | null>(null);
  const [taskTarget, setTaskTarget] = useState<SwitchTask | null>(null);
  const [taskError, setTaskError] = useState<string | null>(null);
  const [stationsError, setStationsError] = useState<string | null>(null);

  const [policyForm] = Form.useForm();
  const [assignForm] = Form.useForm();
  const scanRule = Form.useWatch<ScanRefundRule>('scan_refund_rule', policyForm) || 'time_limited';
  const cardRule = Form.useWatch<CardRefundRule>('card_refund_rule', policyForm) || 'time_limited_prorated';

  const canUpdate = permissions.includes('pricing.rule.update');
  const canCreate = permissions.includes('pricing.rule.create');
  // 列表是按全部站点返回的，所以要从里面挑出本站点那一行，不要指望服务端已经按站点过滤过。
  const policy = policies.find(p => p.station_id === stationId);

  const searchStations = async (keyword: string) => {
    setStationsLoading(true);
    try {
      const result = await apiGet<{ items: Station[] }>('/api/v1/admin/stations', { status: 'active', keyword: keyword || undefined, page: 1, page_size: 100 });
      setStations(result.items || []);
      setStationsError(null);
    } catch (e: any) {
      setStations([]); setStationsError(e?.message || '运营中站点列表读取失败');
    } finally {
      setStationsLoading(false);
    }
  };

  const load = async () => {
    setLoading(true);
    setError('');
    try {
      const list = await apiGet<SwitchTask[]>('/api/v1/admin/settings/switch-tasks', {
        station_id: stationId ?? undefined, status: taskStatus || undefined,
      });
      setTasks(Array.isArray(list) ? list : []);
      const rules = await apiGet<{ items: StationPolicy[]; permissions: string[] }>('/api/v1/admin/settings/station-policies');
      setPolicies((rules.items || []).filter(p => p.station_id === stationId));
      setPermissions(rules.permissions || []);
      if (stationId) {
        const matrix = await apiGet<{ items: DeviceRow[] }>('/api/v1/admin/settings/device-pricing', { station_id: stationId });
        setDevices(matrix.items || []);
      } else {
        setDevices([]);
      }
      // 权限从这次响应里读，不要读 state：首次加载时它还是空的，会让模板池被静默跳过。
      if (rules.permissions?.includes('pricing.rule.create')) {
        const pool = await apiGet<{ items: Template[] }>('/api/v1/admin/settings/pricing-templates');
        setTemplates((pool.items || []).filter(t => t.status === 'active'));
      } else {
        setTemplates([]);
      }
    } catch (e: any) {
      setError(e?.response?.data?.message || e.message || '计费配置读取失败');
    } finally {
      setLoading(false);
    }
  };

  useEffect(() => { void searchStations(''); }, []);
  useEffect(() => { void load(); }, [stationId, taskStatus]);

  const openPolicy = () => {
    if (!stationId) return;
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
    let values: any;
    try {
      values = await policyForm.validateFields();
    } catch {
      return;
    }
    setSaving(true);
    setPolicyError('');
    try {
      await apiPut(`/api/v1/admin/settings/station-policies/${stationId}`, {
        force_recharge: !!values.force_recharge,
        min_balance_cents: toCents(values.min_balance_yuan),
        scan_refund_rule: values.scan_refund_rule,
        scan_refund_path: values.scan_refund_path,
        card_refund_rule: values.card_refund_rule,
        card_refund_path: values.card_refund_path,
        timeout_start_refund: !!values.timeout_start_refund,
        verify_phone_before_charge: !!values.verify_phone_before_charge,
        expected_version: policy?.version || 0,
      });
      message.success('站点策略已保存');
      setPolicyOpen(false);
      await load();
    } catch (e: any) {
      setPolicyError(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const openAssign = (row: DeviceRow) => {
    setAssigning(row);
    setAssignError('');
    setAssignTemplate(null);
    assignForm.resetFields();
    // 这次下发一定带 device_id，服务端比对的也是「这台设备自己那条链」的最新版。
    // 设备还没有独立规则时那条链是空的，版本号就是 0；早先这里回填站点默认的版本号，
    // 于是「第一次给设备单独定价」必然撞锁。
    assignForm.setFieldsValue({ expected_version: row.own_latest_version || 0 });
  };

  const assign = async () => {
    if (!assigning) return;
    if (!assignTemplate) {
      setAssignError('请选择要分配给该设备的计费模板');
      return;
    }
    let expected = 0;
    try {
      expected = Number(assignForm.getFieldValue('expected_version')) || 0;
    } catch {
      expected = 0;
    }
    setSaving(true);
    setAssignError('');
    try {
      await apiPost(`/api/v1/admin/settings/pricing-templates/${assignTemplate}/apply`, {
        station_id: stationId,
        device_id: assigning.device_id,
        request_id: crypto.randomUUID(),
        expected_version: expected,
      });
      message.success(`已为设备 ${assigning.device_id} 分配计费模板`);
      setAssigning(null);
      await load();
    } catch (e: any) {
      setAssignError(e.message || '分配失败');
    } finally {
      setSaving(false);
    }
  };

  const resetDevice = (row: DeviceRow) => Modal.confirm({
    title: '重置为站点默认',
    content: `将设备 ${row.device_id} 自己的计费规则停用、其设备级套餐下架，该设备回落到站点默认计费与套餐。已在充电的订单不受影响。`,
    okText: '重置',
    onOk: async () => {
      await apiPost('/api/v1/admin/settings/device-pricing/reset', { station_id: stationId, device_id: row.device_id });
      message.success('已重置为站点默认');
      await load();
    },
  });

  // 明细取不到时把弹窗留在屏上并显示 LoadError，运营可以原地重试；这里只读，
  // 不用担心误操作。
  const openTask = async (task: SwitchTask) => {
    setTaskTarget(task); setDetail(null); setTaskError(null);
    try {
      setDetail(await apiGet<{ task: SwitchTask; items: SwitchItem[] }>(`/api/v1/admin/settings/switch-tasks/${task.id}`));
    } catch (e: any) {
      setTaskError(e?.message || '切换任务明细读取失败');
    }
  };

  return <div>
    <Space style={{ marginBottom: 12 }} wrap>
      <Select showSearch allowClear aria-label="选择站点" placeholder="选择站点" style={{ width: 320 }}
        value={stationId ?? undefined} loading={stationsLoading} filterOption={false}
        onSearch={value => void searchStations(value)} onChange={value => setStationId(value ?? null)}
        options={stations.map(s => ({ value: s.id, label: s.name }))} />
      <Button onClick={() => void load()} loading={loading}>刷新</Button>
    </Space>
    {error && <LoadError title="站点计费配置加载失败" detail={error} onRetry={() => void load()} />}
    {stationsError && <LoadError title="运营中站点列表加载失败" detail={stationsError} onRetry={() => void searchStations('')} />}
    <Tabs items={[
      {
        key: 'policy', label: '站点策略',
        children: <>
          <Alert type="info" showIcon style={{ marginBottom: 12 }}
            message="站点策略决定的是钱怎么动：用户能不能直接开始、启动失败退不退、退到哪里。它不参与任何一次计费计算。" />
          {!stationId
            ? <div style={{ color: '#999' }}>请先选择站点</div>
            : <Space direction="vertical" style={{ width: '100%' }}>
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
              {canUpdate && <Button type="primary" onClick={openPolicy}>编辑站点策略</Button>}
            </Space>}
        </>,
      },
      {
        key: 'devices', label: '设备分配矩阵',
        children: <>
          <Alert type="info" showIcon style={{ marginBottom: 12 }}
            message="矩阵显示每台设备当前真正生效的计费方式与套餐：没有独立规则的设备显示的是站点默认。分配只影响计费规则，套餐需要到「套餐模板」单独上架。" />
          {!stationId
            ? <div style={{ color: '#999' }}>请先选择站点</div>
            : <Table<DeviceRow> rowKey="device_id" dataSource={devices} loading={loading} scroll={{ x: 1100 }}
              pagination={{ pageSize: 20, showSizeChanger: true, pageSizeOptions: [10, 20, 50, 100], showTotal: n => `共 ${n} 台设备` }}
              columns={[
                { title: '设备编号', dataIndex: 'device_id' },
                { title: '型号', dataIndex: 'model', render: (v?: string) => v || '-' },
                { title: '设备状态', dataIndex: 'device_status', render: (s: string) => <Tag color={s === 'enabled' ? 'green' : 'default'}>{s || '—'}</Tag> },
                {
                  title: '生效计费方式', key: 'template',
                  render: (_: unknown, r: DeviceRow) => {
                    if (!r.spec_json?.mode) {
                      return <span style={{ color: '#999' }}>未配置</span>;
                    }
                    // effective_rule_device_id 为 null 表示这个桩背后那一行是站点默认值，
                    // 而不是它自己的规则。
                    const inherited = r.effective_rule_device_id == null;
                    return <>
                      {modeLabel(r.spec_json.mode)}
                      {r.template_name && <span style={{ color: '#999' }}>（{r.template_name}）</span>}
                      <Tag color={inherited ? 'default' : 'blue'} style={{ marginLeft: 6 }}>{inherited ? '站点默认' : '设备独立'}</Tag>
                    </>;
                  },
                },
                { title: '套餐', dataIndex: 'offer_names', render: (v: string | undefined, r: DeviceRow) => v || (r.offer_count ? `${r.offer_count} 个套餐` : <span style={{ color: '#999' }}>无</span>) },
                {
                  title: '计量能力', key: 'metering',
                  render: (_: unknown, r: DeviceRow) => {
                    const energy = r.reports_energy === true;
                    const power = r.reports_segmented_power === true;
                    if (!energy && !power) {
                      return <Tooltip title="未声明任何计量能力，只能按时长计费">
                        <Tag color="orange">仅时长</Tag>
                      </Tooltip>;
                    }
                    return <Space size={4}>
                      {energy && <Tooltip title="上报电量，可按电量计费"><Tag color="green">电量</Tag></Tooltip>}
                      {power && <Tooltip title="上报分段功率，可按实时功率/最大功率/功率档位计费"><Tag color="green">功率</Tag></Tooltip>}
                    </Space>;
                  },
                },
                {
                  title: '操作', key: 'ops',
                  render: (_: unknown, r: DeviceRow) => <Space>
                    {canCreate && <Button type="link" onClick={() => openAssign(r)}>分配计费方式</Button>}
                    {canUpdate && r.own_rule_id && <Button type="link" danger onClick={() => resetDevice(r)}>重置为站点默认</Button>}
                  </Space>,
                },
              ]} />}
        </>,
      },
      {
        key: 'tasks', label: '切换记录',
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

    <Modal title="编辑站点策略" open={policyOpen} width={720} onCancel={() => setPolicyOpen(false)}
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

    <Modal title={assigning ? `为设备 ${assigning.device_id} 分配计费方式` : '分配计费方式'} open={!!assigning}
      onCancel={() => setAssigning(null)} onOk={() => void assign()} confirmLoading={saving} okText="应用"
      okButtonProps={{ disabled: !assignTemplate }}>
      {assignError && <Alert type="error" showIcon message={assignError} style={{ marginBottom: 12 }} />}
      <Alert type="warning" showIcon style={{ marginBottom: 12 }}
        message="应用会发布为该设备的独立计费规则，脱离站点默认；需要恢复时用列表里的「重置为站点默认」。只发布计费规则，不会上架任何套餐。" />
      <Space direction="vertical" style={{ width: '100%' }}>
        <Select aria-label="选择计费模板" placeholder="选择计费模板" style={{ width: '100%' }}
          value={assignTemplate ?? undefined} onChange={value => setAssignTemplate(value ?? null)}
          options={templates.map(t => ({ value: t.id, label: `${t.name}（${t.spec?.mode || '未配置口径'}）` }))} />
        <Form form={assignForm} name="device_assign" layout="vertical">
          <Form.Item name="expected_version" label="该设备当前规则版本" extra="并发下发时的乐观锁版本号，由矩阵读出，若冲突请刷新后重试。">
            <InputNumber min={0} style={{ width: 200 }} />
          </Form.Item>
        </Form>
      </Space>
    </Modal>

    <Modal title={detail ? `切换任务 ${detail.task.task_no}` : taskTarget ? `切换任务 ${taskTarget.task_no}` : '切换任务'} open={!!taskTarget} width={1000}
      onCancel={() => { setTaskTarget(null); setDetail(null); }}
      footer={<Button onClick={() => { setTaskTarget(null); setDetail(null); }}>关闭</Button>}>
      {taskError ? (
        <LoadError title="切换任务明细加载失败" detail={taskError}
          onRetry={() => { if (taskTarget) void openTask(taskTarget); }} />
      ) : detail && <>
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
