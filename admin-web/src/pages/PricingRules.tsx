import { useEffect, useState } from 'react';
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Space, Table, Tabs, Tag, message } from 'antd';
import { apiGet, apiPost, apiPut } from '../api/client';

// A pricing rule is a template plus an application to a station. The template
// is what an operator edits; applying it writes a station-scoped pricing_rule
// row, which is what billing actually charges against. That row is a copy, so
// editing a template never changes what a station is already charging.
type Period = { period: string; start: string; end: string; electric_price_cents: number; service_price_cents?: number };
type RuleTemplate = {
  id: number; name: string; mode: string; version: number; status: string;
  time_of_use_json: Period[]; service_fee_cents_per_kwh: number;
  service_fee_cents_per_min: number; min_charge_cents: number; applied_stations?: string;
};
type StationRule = {
  id: number; name: string; station_id: number; station_name?: string; version: number;
  status: string; time_of_use_json: Period[]; min_charge_cents: number;
};
type Station = { id: number; code: string; name: string };

// The form works in yuan because that is what an operator reads off a price
// list; the API keeps integer cents.
const yuan = (cents: number) => `¥${(cents / 100).toFixed(2)}`;
const cents = (value: number) => Math.round(Number(value) * 100);

export default function PricingRules() {
  const [templates, setTemplates] = useState<RuleTemplate[]>([]);
  const [rules, setRules] = useState<StationRule[]>([]);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [editing, setEditing] = useState<RuleTemplate | null>(null);
  const [open, setOpen] = useState(false);
  const [publicationError, setPublicationError] = useState('');

  const [applying, setApplying] = useState<RuleTemplate | null>(null);
  const [stations, setStations] = useState<Station[]>([]);
  const [stationsLoading, setStationsLoading] = useState(false);
  const [selectedStation, setSelectedStation] = useState<number | null>(null);
  const [applyError, setApplyError] = useState('');

  const [form] = Form.useForm();
  const mode = Form.useWatch('mode', form);

  const load = async () => {
    setLoading(true);
    try {
      const result = await apiGet<{ items: RuleTemplate[]; permissions: string[] }>('/api/v1/admin/settings/pricing-rule-templates');
      setTemplates(result.items);
      setPermissions(result.permissions || []);
      const applied = await apiGet<{ items: StationRule[] }>('/api/v1/admin/settings/charge-rules');
      setRules(applied.items || []);
    } catch (e: any) {
      message.error(e.message);
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, []);

  const canCreate = permissions.includes('pricing.rule.create');
  const canUpdate = permissions.includes('pricing.rule.update');

  const searchStations = async (keyword: string) => {
    setStationsLoading(true);
    try {
      const result = await apiGet<{ items: Station[] }>('/api/v1/admin/stations', { status: 'active', keyword: keyword || undefined, page: 1, page_size: 50 });
      setStations(result.items || []);
    } catch (e: any) {
      message.error(e.message);
    } finally {
      setStationsLoading(false);
    }
  };

  const edit = async (template?: RuleTemplate) => {
    setEditing(template || null);
    setPublicationError('');
    form.resetFields();
    form.setFieldsValue(template ? {
      ...template,
      service_fee_yuan_per_kwh: template.service_fee_cents_per_kwh / 100,
      service_fee_yuan_per_min: template.service_fee_cents_per_min / 100,
      min_charge_yuan: template.min_charge_cents / 100,
      time_of_use: template.time_of_use_json?.length
        ? template.time_of_use_json.map(p => ({ ...p, electric_price_yuan: p.electric_price_cents / 100, service_price_yuan: p.service_price_cents === undefined ? undefined : p.service_price_cents / 100 }))
        : [{ period: 'flat', start: '00:00', end: '24:00', electric_price_yuan: 0.5 }],
    } : { mode: 'kwh', service_fee_yuan_per_kwh: 0, service_fee_yuan_per_min: 0, min_charge_yuan: 0, time_of_use: [{ period: 'flat', start: '00:00', end: '24:00', electric_price_yuan: 0.5 }] });
    setOpen(true);
  };

  const save = async () => {
    try {
      const values = await form.validateFields();
      setSaving(true);
      setPublicationError('');
      const body = {
        name: values.name,
        mode: values.mode,
        time_of_use: values.time_of_use.map((p: PeriodForm) => ({
          period: p.period,
          start: p.start,
          end: p.end,
          electric_price_cents: cents(p.electric_price_yuan),
          service_price_cents: p.service_price_yuan === undefined ? undefined : cents(p.service_price_yuan),
        })),
        service_fee_cents_per_kwh: cents(values.service_fee_yuan_per_kwh),
        service_fee_cents_per_min: cents(values.service_fee_yuan_per_min),
        min_charge_cents: cents(values.min_charge_yuan),
        version: editing?.version || 0,
      };
      if (editing) await apiPut(`/api/v1/admin/settings/pricing-rule-templates/${editing.id}`, body);
      else await apiPost('/api/v1/admin/settings/pricing-rule-templates', body);
      message.success('计费规则模板已保存');
      setOpen(false);
      await load();
    } catch (e: any) {
      if (!e.errorFields) setPublicationError(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const openApply = async (template: RuleTemplate) => {
    setApplying(template);
    setApplyError('');
    setSelectedStation(null);
    setStations([]);
    await searchStations('');
  };

  const apply = async () => {
    if (!applying) return;
    if (!selectedStation) {
      setApplyError('请选择要应用到的站点');
      return;
    }
    setSaving(true);
    setApplyError('');
    try {
      // The station's current version guards against two operators applying
      // different templates at the same time; one of them is refused.
      const current = rules.filter(r => r.station_id === selectedStation).reduce((max, r) => Math.max(max, r.version), 0);
      const result = await apiPost<{ version: number }>(`/api/v1/admin/settings/pricing-rule-templates/${applying.id}/apply`, {
        station_id: selectedStation,
        request_id: crypto.randomUUID(),
        expected_version: current,
      });
      message.success(`已应用，当前版本 v${result.version}`);
      setApplying(null);
      await load();
    } catch (e: any) {
      setApplyError(e.message || '应用失败');
    } finally {
      setSaving(false);
    }
  };

  const disableTemplate = (template: RuleTemplate) => Modal.confirm({
    title: '停用计费规则模板',
    content: `停用「${template.name}」后不能再应用到新站点。已应用站点的现行规则不受影响，会继续按原规则计费。`,
    okText: '停用',
    onOk: async () => {
      await apiPost(`/api/v1/admin/settings/pricing-rule-templates/${template.id}/disable`);
      message.success('已停用');
      await load();
    },
  });

  const disableRule = (rule: StationRule) => Modal.confirm({
    title: '停用站点计费规则',
    content: `停用「${rule.station_name || rule.station_id}」的 v${rule.version} 后，该站点没有有效规则时将无法发起新支付。已有支付保留原规则快照。`,
    okText: '停用',
    onOk: async () => {
      await apiPost(`/api/v1/admin/settings/charge-rules/${rule.id}/disable`);
      message.success('已停用');
      await load();
    },
  });

  return <>
    <Space style={{ marginBottom: 12 }}>
      <Button onClick={() => void load()} loading={loading}>刷新</Button>
      {canCreate && <Button type="primary" onClick={() => void edit()}>新建规则模板</Button>}
    </Space>
    <Tabs items={[
      {
        key: 'templates',
        label: '规则模板',
        children: <>
          <Alert type="info" showIcon message="模板是通用费率，需要“应用到站点”后才对该站点生效。修改模板不会改变已应用站点的现行费率。电价时段须完整覆盖 00:00–24:00。" style={{ marginBottom: 12 }} />
          <Table rowKey="id" dataSource={templates} loading={loading} scroll={{ x: 1000 }} columns={[
            { title: '名称', render: (_: unknown, r: RuleTemplate) => <>{r.name}<div style={{ color: '#999' }}>v{r.version}</div></> },
            { title: '模式', dataIndex: 'mode', render: (m: string) => m === 'kwh' ? '电量服务费' : m === 'minute' ? '时长服务费' : '电量与时长' },
            { title: '电价时段（元/kWh）', render: (_: unknown, r: RuleTemplate) => <>{Array.isArray(r.time_of_use_json) ? r.time_of_use_json.map((p, i) => <div key={i}>{p.start}–{p.end}：{yuan(p.electric_price_cents)}{p.service_price_cents !== undefined ? ` / 服务费 ${yuan(p.service_price_cents)}` : ''}</div>) : '未配置'}</> },
            { title: '服务费', render: (_: unknown, r: RuleTemplate) => <>{yuan(r.service_fee_cents_per_kwh)}/kWh；{yuan(r.service_fee_cents_per_min)}/分钟</> },
            { title: '起步价', dataIndex: 'min_charge_cents', render: (v: number) => yuan(v) },
            { title: '状态', dataIndex: 'status', render: (s: string) => <Tag color={s === 'active' ? 'green' : 'default'}>{s === 'active' ? '可应用' : '已停用'}</Tag> },
            { title: '已应用站点', dataIndex: 'applied_stations', render: (v?: string) => v || <span style={{ color: '#999' }}>未应用</span> },
            {
              title: '操作',
              render: (_: unknown, r: RuleTemplate) => <Space>
                {canCreate && r.status === 'active' && <Button type="link" onClick={() => void openApply(r)}>应用到站点</Button>}
                {canUpdate && <Button type="link" onClick={() => void edit(r)}>编辑</Button>}
                {canUpdate && r.status === 'active' && <Button type="link" danger onClick={() => disableTemplate(r)}>停用</Button>}
              </Space>,
            },
          ]} />
        </>,
      },
      {
        key: 'stations',
        label: '站点规则',
        children: <Table rowKey="id" dataSource={rules} loading={loading} scroll={{ x: 900 }} columns={[
          { title: '站点', render: (_: unknown, r: StationRule) => <>{r.station_name || '未绑定站点'}（{r.station_id || '—'}）</> },
          { title: '规则名称', dataIndex: 'name' },
          { title: '版本', dataIndex: 'version', render: (v: number) => `v${v}` },
          { title: '电价时段（元/kWh）', render: (_: unknown, r: StationRule) => <>{Array.isArray(r.time_of_use_json) ? r.time_of_use_json.map((p, i) => <div key={i}>{p.start}–{p.end}：{yuan(p.electric_price_cents)}</div>) : '未配置'}</> },
          { title: '起步价', dataIndex: 'min_charge_cents', render: (v: number) => yuan(v) },
          { title: '状态', dataIndex: 'status', render: (s: string) => <Tag color={s === 'active' ? 'green' : 'default'}>{s === 'active' ? '生效中' : '已停用'}</Tag> },
          { title: '操作', render: (_: unknown, r: StationRule) => <Space>{r.status === 'active' && canUpdate && <Button type="link" danger onClick={() => disableRule(r)}>停用</Button>}</Space> },
        ]} />,
      },
    ]} />

    <Modal title={editing ? '编辑计费规则模板' : '新建计费规则模板'} open={open} width={900} onCancel={() => setOpen(false)} onOk={() => void save()} confirmLoading={saving} okText="保存" destroyOnClose>
      {publicationError && <Alert type="error" showIcon message={publicationError} style={{ marginBottom: 12 }} />}
      <Form form={form} name="pricing_template" layout="vertical">
        <Space align="start">
          <Form.Item name="name" label="规则名称" rules={[{ required: true, whitespace: true, max: 128 }]}><Input maxLength={128} /></Form.Item>
          <Form.Item name="mode" label="计费模式" rules={[{ required: true }]}>
            <Select style={{ width: 180 }} options={[{ value: 'kwh', label: '电量服务费' }, { value: 'minute', label: '时长服务费' }, { value: 'mixed', label: '电量与时长服务费' }]} />
          </Form.Item>
        </Space>
        <Space align="start">
          <Form.Item name="service_fee_yuan_per_kwh" label="服务费（元/kWh）" rules={[{ required: true }]}><InputNumber min={0} max={10000} step={0.1} precision={2} addonBefore="¥" /></Form.Item>
          <Form.Item name="service_fee_yuan_per_min" label="服务费（元/分钟）" rules={[{ required: true }]}><InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" /></Form.Item>
          <Form.Item name="min_charge_yuan" label="起步价（元）" rules={[{ required: true }]}><InputNumber min={0} max={10000} step={0.5} precision={2} addonBefore="¥" /></Form.Item>
        </Space>
        <Form.List name="time_of_use">
          {(fields, { add, remove }) => <>
            {fields.map(({ key, name, ...rest }) => <Space key={key} align="start" wrap>
              <Form.Item {...rest} name={[name, 'period']} label="时段名称" rules={[{ required: true, max: 32 }]}><Input style={{ width: 120 }} /></Form.Item>
              <Form.Item {...rest} name={[name, 'start']} label="开始（HH:mm）" rules={[{ required: true, pattern: /^\d{2}:\d{2}$/ }]}><Input style={{ width: 110 }} placeholder="00:00" /></Form.Item>
              <Form.Item {...rest} name={[name, 'end']} label="结束（HH:mm）" rules={[{ required: true, pattern: /^\d{2}:\d{2}$/ }]}><Input style={{ width: 110 }} placeholder="24:00" /></Form.Item>
              <Form.Item {...rest} name={[name, 'electric_price_yuan']} label="电价（元/kWh）" rules={[{ required: true }]}><InputNumber min={0} max={10000} step={0.1} precision={2} addonBefore="¥" /></Form.Item>
              <Form.Item {...rest} name={[name, 'service_price_yuan']} label="时段服务费（可选）"><InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" /></Form.Item>
              <Button onClick={() => remove(name)} disabled={fields.length === 1} style={{ marginTop: 30 }}>移除时段</Button>
            </Space>)}
            <Button onClick={() => add({ period: '', start: '', end: '', electric_price_yuan: 0 })} disabled={fields.length >= 48}>添加时段</Button>
          </>}
        </Form.List>
      </Form>
    </Modal>

    <Modal
      title={applying ? `将「${applying.name}」应用到站点` : '应用到站点'}
      open={!!applying}
      onCancel={() => setApplying(null)}
      onOk={() => void apply()}
      confirmLoading={saving}
      okText="应用"
      okButtonProps={{ disabled: !selectedStation }}
    >
      {applyError && <Alert type="error" showIcon message={applyError} style={{ marginBottom: 12 }} />}
      <Alert type="warning" showIcon message="应用后该站点立即按此规则计费，并生成新版本。若该站点已应用同一模板，请先在“站点规则”中停用现行规则。" style={{ marginBottom: 12 }} />
      <Select
        showSearch
        allowClear
        aria-label="选择站点"
        placeholder="输入站点编码、名称或地址进行筛选"
        value={selectedStation ?? undefined}
        loading={stationsLoading}
        filterOption={false}
        onSearch={value => void searchStations(value)}
        onChange={value => setSelectedStation(value ?? null)}
        options={stations.map(station => ({ value: station.id, label: `${station.name}（${station.code}）` }))}
        style={{ width: '100%' }}
      />
      {stations.length === 0 && !stationsLoading && <div style={{ marginTop: 8, color: '#999' }}>没有匹配的运营中站点</div>}
    </Modal>
  </>;
}

type PeriodForm = { period: string; start: string; end: string; electric_price_yuan: number; service_price_yuan?: number };
