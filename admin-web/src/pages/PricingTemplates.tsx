import { useEffect, useMemo, useState } from 'react';
import { Alert, Button, Collapse, Descriptions, Divider, Form, Input, InputNumber, Modal, Select, Space, Steps, Switch, Table, Tabs, Tag, message } from 'antd';
import { apiGet, apiPost, apiPut } from '../api/client';

// A pricing template is the whole commercial offer in one object: what is
// charged and on what basis, which packages a rider may pick, and what the
// mini program may reveal. It is inert until applied to a station, and applying
// copies all three parts. Editing it afterwards cannot change a station that is
// already running it.
//
// The form is built around one idea: only the fields the chosen basis actually
// uses are shown. A per-session tariff has no use for a time-of-use table, and
// showing one anyway is how operators end up filling in fields that are quietly
// ignored at settlement.

type Basis = 'energy' | 'power_tier' | 'max_power' | 'per_minute' | 'per_session';
type ServiceMode = 'none' | 'energy' | 'minute' | 'minute_power' | 'session';

type Window = { start: string; end: string; cents_per_kwh?: number; service_cents_per_kwh?: number; cents_per_hour_per_kw?: number };
type Tier = { low_w: number; high_w: number; cents_per_hour: number; service_cents_per_hour: number };
type Spec = {
  basis: Basis;
  tier_price_basis?: 'per_hour_at_ceiling' | 'per_kwh';
  windows?: Window[];
  tiers?: Tier[];
  max_power_cents_per_hour_per_kw?: number;
  per_minute_cents?: number;
  per_session_cents?: number;
  service?: { mode: ServiceMode; cents_per_kwh?: number; cents_per_minute?: number; cents_per_hour?: number; cents_per_session?: number };
  free_minutes?: number;
  min_electric_cents?: number;
  loss_rate_bp?: number;
};
type Display = {
  show_energy: boolean; show_power: boolean; show_tariff: boolean; show_fee_split: boolean;
  fee_split_inline: boolean; show_fee_on_end: boolean; show_method: boolean; show_rule: boolean; hide_unit: boolean;
};
type TemplatePackage = { id?: number; name: string; kind: 'amount' | 'package'; price_cents: number; duration_minutes: number; stop_when_full: boolean; status: string };
type Template = { id: number; name: string; remark: string; status: string; version: number; spec?: Spec; display?: Display; packages?: TemplatePackage[]; applied_stations?: string };
type SiteRule = { id: number; name: string; station_id: number; station_name?: string; template_id?: number; version: number; status: string; spec_json?: Spec };
type Station = { id: number; code: string; name: string };

const BASIS_OPTIONS: { value: Basis; label: string; hint: string }[] = [
  { value: 'energy', label: '按电量计费', hint: '费率作用于充进去的电量，单位元/度。适用于普通按量收费。' },
  { value: 'power_tier', label: '按功率档位计费', hint: '按充电功率落入的档位定价，档位越高单价越高，用于鼓励错峰、惩罚占位。' },
  { value: 'max_power', label: '按最大功率计费', hint: '整场按出现过的最高功率 × 时长计费，短暂冲高也按峰值收。' },
  { value: 'per_minute', label: '按时长计费', hint: '不区分电量与时段，按分钟固定计费。' },
  { value: 'per_session', label: '按次计费', hint: '整场固定金额，不看电量。' },
];
const SERVICE_OPTIONS: { value: ServiceMode; label: string; field: string; unit: string }[] = [
  { value: 'none', label: '不收服务费', field: '', unit: '' },
  { value: 'energy', label: '按电量', field: 'cents_per_kwh', unit: '元/度' },
  { value: 'minute', label: '按充电时长', field: 'cents_per_minute', unit: '元/分钟' },
  { value: 'minute_power', label: '按功率档位', field: 'cents_per_hour', unit: '元/小时（随档位）' },
  { value: 'session', label: '按次', field: 'cents_per_session', unit: '元/次' },
];

const yuan = (cents = 0) => `¥${(cents / 100).toFixed(2)}`;
const toCents = (value?: number) => Math.round(Number(value || 0) * 100);
const BASIS_LABEL: Record<Basis, string> = Object.fromEntries(BASIS_OPTIONS.map(o => [o.value, o.label])) as Record<Basis, string>;

export default function PricingTemplates() {
  const [templates, setTemplates] = useState<Template[]>([]);
  const [rules, setRules] = useState<SiteRule[]>([]);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const [editing, setEditing] = useState<Template | null>(null);
  const [step, setStep] = useState(0);
  const [open, setOpen] = useState(false);
  const [formError, setFormError] = useState('');

  const [viewing, setViewing] = useState<Template | null>(null);
  const [applying, setApplying] = useState<Template | null>(null);
  const [stations, setStations] = useState<Station[]>([]);
  const [stationsLoading, setStationsLoading] = useState(false);
  const [selectedStation, setSelectedStation] = useState<number | null>(null);
  const [applyError, setApplyError] = useState('');

  const [form] = Form.useForm();
  const basis = Form.useWatch< Basis>('basis', form) || 'energy';
  const serviceMode = Form.useWatch<ServiceMode>('service_mode', form) || 'none';
  const serviceField = SERVICE_OPTIONS.find(o => o.value === serviceMode)?.field ?? '';
  const usesWindows = basis === 'energy' || basis === 'power_tier';
  const usesTiers = basis === 'power_tier';
  const basisHint = BASIS_OPTIONS.find(o => o.value === basis)?.hint ?? '';

  const load = async () => {
    setLoading(true);
    try {
      const result = await apiGet<{ items: Template[]; permissions: string[] }>('/api/v1/admin/settings/pricing-templates');
      setTemplates(result.items || []);
      setPermissions(result.permissions || []);
      const applied = await apiGet<{ items: SiteRule[] }>('/api/v1/admin/settings/charge-rules');
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

  // toForm flattens the stored spec into the yuan-denominated shape the editor
  // shows. Working in cents in the form is what made the previous screens
  // confusing to read.
  const toForm = (source?: Template) => {
    const spec = source?.spec;
    const windows = spec?.windows?.length ? spec.windows : [{ start: '00:00', end: '24:00', cents_per_kwh: 60 }];
    return {
      name: source?.name ?? '',
      remark: source?.remark ?? '',
      basis: spec?.basis ?? 'energy',
      tier_price_basis: spec?.tier_price_basis ?? 'per_hour_at_ceiling',
      windows: windows.map(w => ({
        start: w.start, end: w.end,
        electric_yuan: (w.cents_per_kwh ?? 0) / 100,
        service_yuan: w.service_cents_per_kwh === undefined ? undefined : w.service_cents_per_kwh / 100,
        max_power_yuan: (w.cents_per_hour_per_kw ?? 60) / 100,
      })),
      tiers: spec?.tiers?.length ? spec.tiers.map(t => ({
        low_w: t.low_w, high_w: t.high_w,
        electric_yuan: t.cents_per_hour / 100, service_yuan: t.service_cents_per_hour / 100,
      })) : [{ low_w: 0, high_w: 200, electric_yuan: 0.17, service_yuan: 0.6 }],
      fallback_yuan: (windows[0]?.cents_per_kwh ?? 100) / 100,
      per_minute_yuan: (spec?.per_minute_cents ?? 0) / 100,
      per_session_yuan: (spec?.per_session_cents ?? 0) / 100,
      service_mode: spec?.service?.mode ?? 'none',
      service_kwh_yuan: (spec?.service?.cents_per_kwh ?? 0) / 100,
      service_min_yuan: (spec?.service?.cents_per_minute ?? 0) / 100,
      service_hour_yuan: (spec?.service?.cents_per_hour ?? 0) / 100,
      service_session_yuan: (spec?.service?.cents_per_session ?? 0) / 100,
      free_minutes: spec?.free_minutes ?? 0,
      min_electric_yuan: (spec?.min_electric_cents ?? 0) / 100,
      loss_rate_percent: ((spec?.loss_rate_bp ?? 0) / 100) || 0,
      packages: source?.packages?.length ? source.packages.map(p => ({
        name: p.name, kind: p.kind,
        price_yuan: p.price_cents / 100, duration_minutes: p.duration_minutes, stop_when_full: p.stop_when_full, status: p.status,
      })) : [],
      display: source?.display ?? {
        show_energy: true, show_power: true, show_tariff: false, show_fee_split: true,
        fee_split_inline: true, show_fee_on_end: true, show_method: true, show_rule: false, hide_unit: false,
      },
    };
  };

  const edit = async (source?: Template) => {
    setEditing(source || null);
    setStep(0);
    setFormError('');
    form.resetFields();
    if (source) {
      // The detail endpoint is the authority; the list row carries only summary
      // fields, so editing straight from the table would silently drop packages.
      try {
        const detail = await apiGet<Template>(`/api/v1/admin/settings/pricing-templates/${source.id}`);
        setEditing(detail);
        form.setFieldsValue(toForm(detail));
      } catch (e: any) {
        message.error(e.message);
        return;
      }
    } else {
      form.setFieldsValue(toForm());
    }
    setOpen(true);
  };

  const view = async (source: Template) => {
    try {
      setViewing(await apiGet<Template>(`/api/v1/admin/settings/pricing-templates/${source.id}`));
    } catch (e: any) {
      message.error(e.message);
    }
  };

  const buildSpec = (values: any): Spec => {
    const service: any = { mode: values.service_mode };
    if (values.service_mode === 'energy') service.cents_per_kwh = toCents(values.service_kwh_yuan);
    if (values.service_mode === 'minute') service.cents_per_minute = toCents(values.service_min_yuan);
    if (values.service_mode === 'minute_power') service.cents_per_hour = toCents(values.service_hour_yuan);
    if (values.service_mode === 'session') service.cents_per_session = toCents(values.service_session_yuan);
    const spec: Spec = { basis: values.basis, service, free_minutes: values.free_minutes || 0, min_electric_cents: toCents(values.min_electric_yuan), loss_rate_bp: Math.round((values.loss_rate_percent || 0) * 100) };
    if (values.basis === 'power_tier') {
      spec.tier_price_basis = values.tier_price_basis;
      spec.tiers = (values.tiers || []).map((t: any) => ({
        low_w: Number(t.low_w), high_w: Number(t.high_w),
        cents_per_hour: toCents(t.electric_yuan), service_cents_per_hour: toCents(t.service_yuan),
      }));
      // Above the top tier the window rate is the fallback, so it is written to
      // every window rather than being a second, separately editable number
      // that could disagree with the visible table.
      const fallback = toCents(values.fallback_yuan);
      spec.windows = (values.windows || []).map((w: any) => ({ start: w.start, end: w.end, cents_per_kwh: fallback }));
    } else if (values.basis === 'energy') {
      spec.windows = (values.windows || []).map((w: any) => ({
        start: w.start, end: w.end, cents_per_kwh: toCents(w.electric_yuan),
        service_cents_per_kwh: w.service_yuan === undefined ? undefined : toCents(w.service_yuan),
      }));
    } else if (values.basis === 'max_power') {
      spec.windows = [{ start: '00:00', end: '24:00', cents_per_hour_per_kw: toCents((values.windows || [])[0]?.max_power_yuan) }];
    } else if (values.basis === 'per_minute') {
      spec.per_minute_cents = toCents(values.per_minute_yuan);
    } else if (values.basis === 'per_session') {
      spec.per_session_cents = toCents(values.per_session_yuan);
    }
    return spec;
  };

  const save = async () => {
    try {
      const values = await form.validateFields();
      setSaving(true);
      setFormError('');
      const body = {
        name: values.name,
        remark: values.remark ?? '',
        spec: buildSpec(values),
        display: values.display,
        packages: (values.packages || []).map((p: any) => ({
          name: p.name, kind: p.kind,
          price_cents: p.kind === 'amount' ? toCents(p.price_yuan) : 0,
          duration_minutes: p.kind === 'package' ? Number(p.duration_minutes || 0) : 0,
          stop_when_full: !!p.stop_when_full,
          status: p.status || 'active',
        })),
        version: editing?.version || 0,
      };
      if (editing) await apiPut(`/api/v1/admin/settings/pricing-templates/${editing.id}`, body);
      else await apiPost('/api/v1/admin/settings/pricing-templates', body);
      message.success('计费模板已保存');
      setOpen(false);
      await load();
    } catch (e: any) {
      if (!e.errorFields) setFormError(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const openApply = async (template: Template) => {
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
      const current = rules.filter(r => r.station_id === selectedStation).reduce((max, r) => Math.max(max, r.version), 0);
      const result = await apiPost<{ version: number }>(`/api/v1/admin/settings/pricing-templates/${applying.id}/apply`, {
        station_id: selectedStation, request_id: crypto.randomUUID(), expected_version: current,
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

  const disableTemplate = (template: Template) => Modal.confirm({
    title: '停用计费模板',
    content: `停用「${template.name}」后不能再应用到新站点。已应用站点的现行规则与套餐不受影响，会继续按原样计费。`,
    okText: '停用',
    onOk: async () => {
      await apiPost(`/api/v1/admin/settings/pricing-templates/${template.id}/disable`);
      message.success('已停用');
      await load();
    },
  });

  const copyTemplate = (template: Template) => Modal.confirm({
    title: '复制计费模板',
    content: <Input placeholder="新模板名称" maxLength={64} />,
    icon: null,
    okText: '复制',
    onOk: async () => {
      const name = (document.querySelector('.ant-modal-confirm-content input') as HTMLInputElement)?.value?.trim();
      if (!name) {
        message.error('请填写新模板名称');
        throw new Error('name required');
      }
      await apiPost(`/api/v1/admin/settings/pricing-templates/${template.id}/copy`, { name });
      message.success('已复制');
      await load();
    },
  });

  const disableRule = (rule: SiteRule) => Modal.confirm({
    title: '停用站点计费规则',
    content: `停用「${rule.station_name || rule.station_id}」的 v${rule.version} 后，该站点没有有效规则时将无法发起新支付。已有支付保留原规则快照。`,
    okText: '停用',
    onOk: async () => {
      await apiPost(`/api/v1/admin/settings/charge-rules/${rule.id}/disable`);
      message.success('已停用');
      await load();
    },
  });

  const stepItems = useMemo(() => [
    {
      key: 'basic', label: '基本信息',
      children: <Form form={form} name="pricing_template" layout="vertical">
        <Space align="start" wrap>
          <Form.Item name="name" label="模板名称" rules={[{ required: true, whitespace: true, max: 64 }]}><Input maxLength={64} placeholder="如：二轮标准梯度价" /></Form.Item>
          <Form.Item name="remark" label="模板备注" rules={[{ max: 255 }]}><Input maxLength={255} placeholder="选填，说明适用范围" /></Form.Item>
        </Space>
      </Form>,
    },
    {
      key: 'tariff', label: '计费口径',
      children: <Form form={form} name="pricing_tariff" layout="vertical">
        <Form.Item name="basis" label="计费方式" rules={[{ required: true }]}>
          <Select style={{ width: 280 }} options={BASIS_OPTIONS.map(o => ({ value: o.value, label: o.label }))} />
        </Form.Item>
        {basisHint && <Alert type="info" showIcon message={basisHint} style={{ marginBottom: 12 }} />}

        {usesTiers && <>
          <Form.Item name="tier_price_basis" label="档位单价口径" rules={[{ required: true }]}>
            <Select style={{ width: 320 }} options={[
              { value: 'per_hour_at_ceiling', label: '元/小时，按档位上限折算' },
              { value: 'per_kwh', label: '元/度，直接作为电价' },
            ]} />
          </Form.Item>
          <Alert type="warning" showIcon style={{ marginBottom: 12 }}
            message="档位单价的换算方式会直接改变账单金额，切换前请先用测试站点验算。" />
          <Form.List name="tiers">
            {(fields, { add, remove }) => <>
              <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, minmax(0,1fr)) auto', gap: 8, fontWeight: 600, marginBottom: 4 }}>
                <span>低档（瓦）</span><span>高档（瓦）</span><span>电费单价（元/小时）</span><span>服务费单价（元/小时）</span><span />
              </div>
              {fields.map(({ key, name, ...rest }) => <Space key={key} align="start" style={{ display: 'flex' }}>
                <Form.Item {...rest} name={[name, 'low_w']} rules={[{ required: true }]}><InputNumber min={0} max={100000} style={{ width: 120 }} /></Form.Item>
                <Form.Item {...rest} name={[name, 'high_w']} rules={[{ required: true }]}><InputNumber min={1} max={100000} style={{ width: 120 }} /></Form.Item>
                <Form.Item {...rest} name={[name, 'electric_yuan']} rules={[{ required: true }]}><InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: 160 }} /></Form.Item>
                <Form.Item {...rest} name={[name, 'service_yuan']}><InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: 160 }} /></Form.Item>
                <Button onClick={() => remove(name)} disabled={fields.length === 1} style={{ marginTop: 8 }}>删除档位</Button>
              </Space>)}
              <Button onClick={() => add({ low_w: 0, high_w: 0, electric_yuan: 0, service_yuan: 0 })} disabled={fields.length >= 32}>添加档位</Button>
            </>}
          </Form.List>
          <Divider />
        </>}

        {usesWindows && <Form.List name="windows">
          {(fields, { add, remove }) => <>
            <div style={{ display: 'grid', gridTemplateColumns: 'repeat(4, minmax(0,1fr)) auto', gap: 8, fontWeight: 600, marginBottom: 4 }}>
              <span>开始（HH:mm）</span><span>结束（HH:mm）</span>
              <span>{usesTiers ? '超档电费单价（元/度）' : '电价（元/度）'}</span>
              <span>{serviceMode === 'energy' ? '时段服务费（元/度）' : ''}</span><span />
            </div>
            {fields.map(({ key, name, ...rest }) => <Space key={key} align="start" style={{ display: 'flex' }}>
              <Form.Item {...rest} name={[name, 'start']} rules={[{ required: true, pattern: /^\d{2}:\d{2}$/ }]}><Input style={{ width: 110 }} placeholder="00:00" /></Form.Item>
              <Form.Item {...rest} name={[name, 'end']} rules={[{ required: true, pattern: /^\d{2}:\d{2}$/ }]}><Input style={{ width: 110 }} placeholder="24:00" /></Form.Item>
              {usesTiers
                ? <Form.Item {...rest} name={[name, 'electric_yuan']}><InputNumber min={0} max={10000} step={0.01} precision={2} style={{ display: 'none' }} /></Form.Item>
                : <Form.Item {...rest} name={[name, 'electric_yuan']} rules={[{ required: true }]}><InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: 150 }} /></Form.Item>}
              <Form.Item {...rest} name={[name, 'service_yuan']}>
                {serviceMode === 'energy'
                  ? <InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: 150 }} />
                  : <InputNumber style={{ display: 'none' }} />}
              </Form.Item>
              <Button onClick={() => remove(name)} disabled={fields.length === 1} style={{ marginTop: 8 }}>移除时段</Button>
            </Space>)}
            <Button onClick={() => add({ start: '', end: '', electric_yuan: 0 })} disabled={fields.length >= 48}>添加时段</Button>
            <Alert type="info" showIcon style={{ marginTop: 12 }}
              message="时段必须完整覆盖 00:00–24:00 且互不重叠，否则模板保存时会被拒绝。" />
          </>}
        </Form.List>}

        {usesTiers && <Form.Item name="fallback_yuan" label="超过最大档位的电费单价（元/度）" rules={[{ required: true }]}
          extra="充电功率超过最高档时，超出部分按此单价乘电量计费。">
          <InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: 220 }} />
        </Form.Item>}

        {basis === 'max_power' && <Form.List name="windows">
          {(fields) => <Form.Item {...fields[0]} name={[0, 'max_power_yuan']} label="最大功率单价（元/小时·kW）"
            rules={[{ required: true }]} extra="整场按出现过的最高功率 × 充电小时 × 此单价计费。">
            <InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: 220 }} />
          </Form.Item>}
        </Form.List>}

        {basis === 'per_minute' && <Form.Item name="per_minute_yuan" label="每分钟单价（元/分钟）" rules={[{ required: true }]}>
          <InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: 220 }} />
        </Form.Item>}

        {basis === 'per_session' && <Form.Item name="per_session_yuan" label="每场固定金额（元/次）" rules={[{ required: true }]}>
          <InputNumber min={0} max={10000} step={0.5} precision={2} addonBefore="¥" style={{ width: 220 }} />
        </Form.Item>}

        <Divider orientation="left" plain>服务费</Divider>
        <Space align="start" wrap>
          <Form.Item name="service_mode" label="服务费口径" rules={[{ required: true }]}>
            <Select style={{ width: 220 }} options={SERVICE_OPTIONS.map(o => ({ value: o.value, label: o.label }))} />
          </Form.Item>
          {serviceMode === 'energy' && <Form.Item name="service_kwh_yuan" label="服务费（元/度）" rules={[{ required: true }]}><InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" /></Form.Item>}
          {serviceMode === 'minute' && <Form.Item name="service_min_yuan" label="服务费（元/分钟）" rules={[{ required: true }]}><InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" /></Form.Item>}
          {serviceMode === 'minute_power' && <Form.Item name="service_hour_yuan" label="服务费（元/小时）" rules={[{ required: true }]}
            extra="实际取值来自上方各档位的服务费单价，此处仅作兜底。"><InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" /></Form.Item>}
          {serviceMode === 'session' && <Form.Item name="service_session_yuan" label="服务费（元/次）" rules={[{ required: true }]}><InputNumber min={0} max={10000} step={0.5} precision={2} addonBefore="¥" /></Form.Item>}
        </Space>

        <Collapse ghost style={{ marginTop: 8 }} items={[{ key: 'policy', label: '全局策略（点击展开）', children: <Space direction="vertical" style={{ width: '100%' }}>
          <Form.Item name="free_minutes" label="规定时间内免费充电（分钟）" extra="在此分钟内结束充电，本次不计费。0 表示不启用。">
            <InputNumber min={0} max={1440} style={{ width: 200 }} />
          </Form.Item>
          <Form.Item name="min_electric_yuan" label="电费最低消费（元）" extra="电费低于该金额时按该金额收取，仅针对电费，不含服务费。">
            <InputNumber min={0} max={10000} step={0.5} precision={2} addonBefore="¥" style={{ width: 200 }} />
          </Form.Item>
          <Form.Item name="loss_rate_percent" label="电损率（%）" extra="按此比例放大可计费电量，弥补线路损耗。">
            <InputNumber min={0} max={10} step={0.1} precision={2} addonAfter="%" style={{ width: 200 }} />
          </Form.Item>
        </Space> }]} />
      </Form>,
    },
    {
      key: 'packages', label: '套餐与展示',
      children: <Form form={form} name="pricing_packages" layout="vertical">
        <Form.List name="packages">
          {(fields, { add, remove }) => <>
            {fields.map(({ key, name, ...rest }) => <Space key={key} align="start" wrap>
              <Form.Item {...rest} name={[name, 'name']} label="套餐名称" rules={[{ required: true, max: 64 }]}><Input style={{ width: 160 }} maxLength={64} /></Form.Item>
              <Form.Item {...rest} name={[name, 'kind']} label="类型" rules={[{ required: true }]}>
                <Select style={{ width: 160 }} options={[{ value: 'amount', label: '按金额' }, { value: 'package', label: '按时长' }]} />
              </Form.Item>
              <Form.Item shouldUpdate noStyle>
                {() => {
                  const kind = form.getFieldValue(['packages', name, 'kind']);
                  return kind === 'amount'
                    ? <Form.Item {...rest} name={[name, 'price_yuan']} label="金额（元）" rules={[{ required: true }]}><InputNumber min={0.01} max={10000} step={1} precision={2} addonBefore="¥" style={{ width: 150 }} /></Form.Item>
                    : <Form.Item {...rest} name={[name, 'duration_minutes']} label="时长（分钟）" rules={[{ required: true }]}><InputNumber min={1} max={600} style={{ width: 150 }} /></Form.Item>;
                }}
              </Form.Item>
              <Form.Item {...rest} name={[name, 'stop_when_full']} label="充满自停" valuePropName="checked"><Switch /></Form.Item>
              <Button onClick={() => remove(name)} danger style={{ marginTop: 30 }}>删除</Button>
            </Space>)}
            <Button onClick={() => add({ name: '', kind: 'amount', price_yuan: 1, stop_when_full: false, status: 'active' })} disabled={fields.length >= 32}>添加套餐</Button>
            <Alert type="info" showIcon style={{ marginTop: 12 }}
              message="套餐与计费口径同属一个模板，一起应用到站点，避免站点卖着按旧费率算的套餐。" />
          </>}
        </Form.List>

        <Divider orientation="left" plain>用户界面展示</Divider>
        <Form.Item name={['display', 'show_energy']} label="显示充电电量" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name={['display', 'show_power']} label="展示充电功率" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name={['display', 'show_tariff']} label="显示时段计费详情" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name={['display', 'show_method']} label="显示计费方式" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name={['display', 'show_rule']} label="展示规则说明" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name={['display', 'show_fee_split']} label="订单详情显示电费与服务费" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item noStyle shouldUpdate={(a, b) => a.display?.show_fee_split !== b.display?.show_fee_split}>
          {() => form.getFieldValue(['display', 'show_fee_split']) && <Form.Item name={['display', 'fee_split_inline']} label="费用直接显示在支付金额后" valuePropName="checked">
            <Switch checkedChildren="直接显示" unCheckedChildren="隐藏展示" />
          </Form.Item>}
        </Form.Item>
        <Form.Item name={['display', 'show_fee_on_end']} label="结束充电推送显示费用" valuePropName="checked"><Switch /></Form.Item>
        <Form.Item name={['display', 'hide_unit']} label="隐藏单位" valuePropName="checked"><Switch /></Form.Item>
      </Form>,
    },
  ], [form, basis, serviceMode, usesWindows, usesTiers]);

  const renderSpecSummary = (spec?: Spec) => {
    if (!spec) return '—';
    const parts: string[] = [];
    if (spec.basis === 'per_session') parts.push(`每场 ${yuan(spec.per_session_cents)}`);
    else if (spec.basis === 'per_minute') parts.push(`${yuan(spec.per_minute_cents)}/分钟`);
    else if (spec.basis === 'max_power') parts.push(`${yuan(spec.windows?.[0]?.cents_per_hour_per_kw)}/小时·kW（按最大功率）`);
    else if (spec.basis === 'power_tier') parts.push(`${spec.tiers?.length ?? 0} 个功率档位`);
    else parts.push((spec.windows || []).map(w => `${w.start}–${w.end} ${yuan(w.cents_per_kwh)}`).join('；'));
    if (spec.service?.mode && spec.service.mode !== 'none') parts.push(`服务费：${SERVICE_OPTIONS.find(o => o.value === spec.service!.mode)?.label}`);
    return parts.join('，');
  };

  return <>
    <Space style={{ marginBottom: 12 }}>
      <Button onClick={() => void load()} loading={loading}>刷新</Button>
      {canCreate && <Button type="primary" onClick={() => void edit()}>新建计费模板</Button>}
    </Space>
    <Tabs items={[
      {
        key: 'templates', label: '计费模板',
        children: <>
          <Alert type="info" showIcon style={{ marginBottom: 12 }}
            message="模板包含计费口径、套餐和用户端展示开关，需要“应用到站点”后才生效。修改模板不会改变已应用站点的现行计费。" />
          <Table rowKey="id" dataSource={templates} loading={loading} scroll={{ x: 1100 }} columns={[
            { title: '名称', render: (_: unknown, r: Template) => <>{r.name}<div style={{ color: '#999' }}>v{r.version}{r.remark ? ` · ${r.remark}` : ''}</div></> },
            { title: '计费方式', render: (_: unknown, r: Template) => <Tag color="blue">{r.spec ? BASIS_LABEL[r.spec.basis] : '—'}</Tag> },
            { title: '费率', render: (_: unknown, r: Template) => renderSpecSummary(r.spec) },
            { title: '状态', dataIndex: 'status', render: (s: string) => <Tag color={s === 'active' ? 'green' : 'default'}>{s === 'active' ? '可应用' : '已停用'}</Tag> },
            { title: '已应用站点', dataIndex: 'applied_stations', render: (v?: string) => v || <span style={{ color: '#999' }}>未应用</span> },
            {
              title: '操作', render: (_: unknown, r: Template) => <Space>
                <Button type="link" onClick={() => void view(r)}>查看</Button>
                {canCreate && r.status === 'active' && <Button type="link" onClick={() => void openApply(r)}>应用到站点</Button>}
                {canUpdate && <Button type="link" onClick={() => void edit(r)}>编辑</Button>}
                {canCreate && <Button type="link" onClick={() => copyTemplate(r)}>复制</Button>}
                {canUpdate && r.status === 'active' && <Button type="link" danger onClick={() => disableTemplate(r)}>停用</Button>}
              </Space>,
            },
          ]} />
        </>,
      },
      {
        key: 'stations', label: '站点计费',
        children: <>
          <Alert type="warning" showIcon style={{ marginBottom: 12 }}
            message="停用站点规则后，该站点在无其他有效规则时将无法发起新支付；已付款订单保留原有规则快照，不受影响。" />
          <Table rowKey="id" dataSource={rules} loading={loading} scroll={{ x: 900 }} columns={[
            { title: '站点', render: (_: unknown, r: SiteRule) => <>{r.station_name || '未绑定站点'}（{r.station_id || '—'}）</> },
            { title: '规则名称', dataIndex: 'name' },
            { title: '版本', dataIndex: 'version', render: (v: number) => `v${v}` },
            { title: '计费方式', render: (_: unknown, r: SiteRule) => r.spec_json ? BASIS_LABEL[r.spec_json.basis] : '—' },
            { title: '费率', render: (_: unknown, r: SiteRule) => renderSpecSummary(r.spec_json) },
            { title: '状态', dataIndex: 'status', render: (s: string) => <Tag color={s === 'active' ? 'green' : 'default'}>{s === 'active' ? '生效中' : '已停用'}</Tag> },
            { title: '操作', render: (_: unknown, r: SiteRule) => <Space>{r.status === 'active' && canUpdate && <Button type="link" danger onClick={() => disableRule(r)}>停用</Button>}</Space> },
          ]} />
        </>,
      },
    ]} />

    <Modal
      title={editing ? `编辑计费模板 · ${editing.name}` : '新建计费模板'}
      open={open} width={1080} onCancel={() => setOpen(false)}
      confirmLoading={saving}
      footer={[
        <Button key="cancel" onClick={() => setOpen(false)}>取消</Button>,
        step > 0 && <Button key="back" onClick={() => setStep(step - 1)}>上一步</Button>,
        <Button key="next" type="primary" loading={saving} onClick={async () => {
          if (step < 2) {
            try {
              await form.validateFields(step === 0 ? ['name'] : []);
            } catch (e: any) {
              if (e.errorFields) return;
              setFormError(e.message);
              return;
            }
            setStep(step + 1);
            return;
          }
          await save();
        }}>{step === 2 ? '保存' : '下一步'}</Button>,
      ]}
      destroyOnClose
    >
      {formError && <Alert type="error" showIcon message={formError} style={{ marginBottom: 12 }} />}
      <Steps current={step} size="small" style={{ marginBottom: 16 }} items={[{ title: '基本信息' }, { title: '计费口径' }, { title: '套餐与展示' }]} />
      {step < 2
        ? <Collapse activeKey={[stepItems[step].key]} ghost items={[stepItems[step]]} />
        : <Collapse activeKey={[stepItems[2].key]} ghost items={[stepItems[2]]} />}
      {step === 1 && serviceField === 'cents_per_hour' && <Alert type="info" showIcon style={{ marginTop: 12 }} message="按功率档位收取服务费时，实际单价取自上方各档位的服务费单价。" />}
    </Modal>

    <Modal title={viewing ? `计费模板详情 · ${viewing.name}` : '计费模板详情'} open={!!viewing} width={860}
      onCancel={() => setViewing(null)} footer={<Button onClick={() => setViewing(null)}>关闭</Button>}>
      {viewing && <>
        <Descriptions size="small" column={2} bordered items={[
          { key: 'name', label: '名称', children: viewing.name },
          { key: 'status', label: '状态', children: viewing.status === 'active' ? '可应用' : '已停用' },
          { key: 'remark', label: '备注', children: viewing.remark || '—', span: 2 },
          { key: 'version', label: '版本', children: `v${viewing.version}` },
          { key: 'basis', label: '计费方式', children: viewing.spec ? BASIS_LABEL[viewing.spec.basis] : '—' },
        ]} />
        <Divider orientation="left" plain>费率</Divider>
        <div>{renderSpecSummary(viewing.spec)}</div>
        {viewing.spec?.basis === 'power_tier' && viewing.spec.tiers && <Table size="small" pagination={false} style={{ marginTop: 8 }}
          rowKey={(_, i) => String(i)} dataSource={viewing.spec.tiers} columns={[
            { title: '低档（瓦）', dataIndex: 'low_w' },
            { title: '高档（瓦）', dataIndex: 'high_w' },
            { title: '电费单价（元/小时）', render: (_: unknown, r: Tier) => yuan(r.cents_per_hour) },
            { title: '服务费单价（元/小时）', render: (_: unknown, r: Tier) => yuan(r.service_cents_per_hour) },
          ]} />}
        {viewing.spec && <div style={{ marginTop: 8, color: '#666' }}>
          规定时间内免费：{viewing.spec.free_minutes || 0} 分钟 · 电费最低消费：{yuan(viewing.spec.min_electric_cents)} · 电损率：{((viewing.spec.loss_rate_bp || 0) / 100).toFixed(2)}%
        </div>}
        <Divider orientation="left" plain>套餐（{(viewing.packages || []).length}）</Divider>
        <Table size="small" pagination={false} rowKey="id" dataSource={viewing.packages || []} columns={[
          { title: '名称', dataIndex: 'name' },
          { title: '类型', dataIndex: 'kind', render: (k: string) => k === 'amount' ? '按金额' : '按时长' },
          { title: '金额', render: (_: unknown, r: TemplatePackage) => r.price_cents ? yuan(r.price_cents) : '—' },
          { title: '时长', render: (_: unknown, r: TemplatePackage) => r.duration_minutes ? `${r.duration_minutes} 分钟` : '—' },
          { title: '充满自停', render: (_: unknown, r: TemplatePackage) => r.stop_when_full ? '是' : '否' },
        ]} />
        <Divider orientation="left" plain>用户端展示</Divider>
        <div>{viewing.display ? Object.entries(viewing.display).filter(([, v]) => v).map(([k]) => k).join('、') || '全部关闭' : '—'}</div>
      </>}
    </Modal>

    <Modal title={applying ? `将「${applying.name}」应用到站点` : '应用到站点'} open={!!applying}
      onCancel={() => setApplying(null)} onOk={() => void apply()} confirmLoading={saving} okText="应用"
      okButtonProps={{ disabled: !selectedStation }}>
      {applyError && <Alert type="error" showIcon message={applyError} style={{ marginBottom: 12 }} />}
      <Alert type="warning" showIcon style={{ marginBottom: 12 }}
        message="应用后该站点立即按此模板计费，并生成新版本；模板内的套餐会同时在该站点上架。若该站点已应用同一模板，请先在“站点计费”中停用现行规则。" />
      <Select showSearch allowClear aria-label="选择站点" placeholder="输入站点编码、名称或地址进行筛选"
        value={selectedStation ?? undefined} loading={stationsLoading} filterOption={false}
        onSearch={value => void searchStations(value)} onChange={value => setSelectedStation(value ?? null)}
        options={stations.map(s => ({ value: s.id, label: `${s.name}（${s.code}）` }))} style={{ width: '100%' }} />
      {stations.length === 0 && !stationsLoading && <div style={{ marginTop: 8, color: '#999' }}>没有匹配的运营中站点</div>}
    </Modal>
  </>;
}
