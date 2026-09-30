import { useEffect, useState } from 'react';
import { Alert, Button, Collapse, Descriptions, Divider, Form, Input, InputNumber, Modal, Popconfirm, Select, Space, Spin, Steps, Switch, Table, Tag, message } from 'antd';
import { apiGet, apiPost, apiPut } from '../api/client';
import { LoadError } from '../components/LoadError';
import PeriodTimeInput from './pricing/PeriodTimeInput';
import PricingPreview, { type PreviewDraft } from './pricing/PricingPreview';
import {
  DEFAULT_DISPLAY, MODE_META, MODE_OPTIONS, SERVICE_OPTIONS, blankPeriod, describeDisplay, describeSpec, defaultSpecForm,
  canInsertTier, clockToMinute, convertLegacyPowerForm, effectiveTierElectricCents, formToSpec, insertTier, isLegacyPowerPricing, isServerBilled, minuteToClock, modeLabel, removePeriod, removeTier,
  splitPeriod, suggestedSplitMinute,
  specToForm, tierRateUnits, validateSpecForm, type ChargeMode, type PeriodForm, type SpecForm, type Station, type Template, type TierForm,
} from './pricing/model';

// 下发模板时用来查乐观锁版本号的那一列设备。字段取自
// GET /api/v1/admin/settings/device-pricing，口径与服务端读锁完全一致（不过滤 status）。
type ScopeDevice = { device_id: string; own_latest_version?: number };

// 电价模板就是电价本身：收多少、按什么口径收、以及小程序允许露出什么。套餐是另一个池子
// （见 PackageTemplates），因为预付封顶自己结算，无论当前跑的是哪套电价都有效。
//
// 向导第二步只显示所选计费方式真正会读到的字段。设备侧计费的方式在整个系统里都没有
// 费率，所以它干脆一个费率输入框都不给，而不是给一堆会被静默忽略的输入框。

export default function PricingTemplates() {
  const [templates, setTemplates] = useState<Template[]>([]);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const [editing, setEditing] = useState<Template | null>(null);
  const [step, setStep] = useState(0);
  const [open, setOpen] = useState(false);
  const [formError, setFormError] = useState('');
  const [localErrors, setLocalErrors] = useState<string[]>([]);
  const [timeErrors, setTimeErrors] = useState<Record<number, string>>({});
  const [periodAction, setPeriodAction] = useState<{ key: number; kind: 'split' | 'delete'; time: string } | null>(null);
  const [splitError, setSplitError] = useState('');
  const [previewDraft, setPreviewDraft] = useState<PreviewDraft | null>(null);

  const [viewing, setViewing] = useState<Template | null>(null);
  const [viewError, setViewError] = useState<string | null>(null);
  const [viewLoading, setViewLoading] = useState(false);
  const [listError, setListError] = useState<string | null>(null);
  const [stationsError, setStationsError] = useState<string | null>(null);
  const [applying, setApplying] = useState<Template | null>(null);
  const [stations, setStations] = useState<Station[]>([]);
  const [stationsLoading, setStationsLoading] = useState(false);
  const [selectedStation, setSelectedStation] = useState<number | null>(null);
  const [applyDevice, setApplyDevice] = useState('');
  const [applyError, setApplyError] = useState('');
  // 所选站点当前两条规则链的最新版号：整站链一份、每台设备各一份。
  const [stationLatestVersion, setStationLatestVersion] = useState(0);
  const [scopeDevices, setScopeDevices] = useState<ScopeDevice[]>([]);

  const [form] = Form.useForm();
  const mode = Form.useWatch<ChargeMode>('mode', { form, preserve: true }) || 'server_realtime_power';
  const serviceBasis = Form.useWatch<string>('service_basis', { form, preserve: true }) || 'none';
  const tierPriceBasis = Form.useWatch<SpecForm['tier_price_basis']>('tier_price_basis', { form, preserve: true }) || 'per_kwh';
  const multiplierOn = !!Form.useWatch('multiplier_on', { form, preserve: true });
  const showFeeSplit = !!Form.useWatch(['display', 'show_fee_split'], { form, preserve: true });
  // preserve 必须为 true：默认取值走 getFieldsValue()，那份对象只由「已挂载的
  // Form.Item」拼出来。periods 的长度决定了要渲染几张时段卡片，于是形成死锁——
  // 新增的第二段没有卡片就没有字段注册，没有注册 useWatch 就只还回第一段，
  // 卡片永远画不出来，保存时更会把它悄悄丢掉。读原始 store 才拿得到完整数组。
  const periods: PeriodForm[] = Form.useWatch('periods', { form, preserve: true }) || [];
  const energyBasis = mode === 'server_energy';
  const server = isServerBilled(mode);
  const tierUnits = tierRateUnits(mode);
  const legacyPricing = mode === 'server_realtime_power' && tierPriceBasis !== 'per_kwh';

  const load = async () => {
    setLoading(true);
    try {
      const result = await apiGet<{ items: Template[]; permissions: string[] }>('/api/v1/admin/settings/pricing-templates');
      setTemplates(result.items || []);
      setPermissions(result.permissions || []);
      setListError(null);
    } catch (e: any) {
      setTemplates([]); setPermissions([]); setListError(e?.message || '计费模板列表读取失败');
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
      setStationsError(null);
    } catch (e: any) {
      // 站点下拉读不到时不能只把列表留空：弹窗里那行「没有匹配的运营中站点」会把
      // 接口故障说成「确实没有站点」，于是运营以为选不中、放弃应用模板。
      setStations([]); setStationsError(e?.message || '运营中站点列表读取失败');
    } finally {
      setStationsLoading(false);
    }
  };

  const openEditor = async (source?: Template) => {
    setFormError('');
    setLocalErrors([]);
    setTimeErrors({}); setPeriodAction(null); setSplitError('');
    setStep(0);
    if (source) {
      // 详情接口才是权威；列表行只带摘要字段，所以从表格直接编辑会把电价本身丢掉。
      try {
        const detail = await apiGet<Template>(`/api/v1/admin/settings/pricing-templates/${source.id}`);
        setEditing(detail);
        form.setFieldsValue({ ...specToForm(detail.spec), name: detail.name, remark: detail.remark, display: detail.display || DEFAULT_DISPLAY });
      } catch (e: any) {
        // 向导是多步表单，详情没到手时不开弹窗——空壳向导一旦被误提交会覆盖模板。
        // 这是「打开编辑」这个动作没做成，用 toast 提示，不占用整块版面。
        message.error(`编辑计费模板加载失败：${e?.message || '未知原因'}`);
        return;
      }
    } else {
      setEditing(null);
      form.resetFields();
      form.setFieldsValue({ ...defaultSpecForm('server_realtime_power'), name: '', remark: '', display: DEFAULT_DISPLAY });
    }
    setOpen(true);
  };

  // 先用列表行把弹窗打开（标题里有名字），详情取不到时弹窗里显示 LoadError，
  // 运营可以直接在原地重试，不必关掉再点一次。
  const view = async (source: Template) => {
    setViewing(source); setViewError(null); setViewLoading(true);
    try {
      setViewing(await apiGet<Template>(`/api/v1/admin/settings/pricing-templates/${source.id}`));
    } catch (e: any) {
      setViewError(e?.message || '计费模板详情读取失败');
    } finally {
      setViewLoading(false);
    }
  };

  const switchMode = (next: ChargeMode) => {
    const previous = form.getFieldValue('mode') as ChargeMode;
    if (previous === next) return;
    setTimeErrors({}); setPeriodAction(null); setSplitError('');
    form.setFieldsValue(specToForm({ mode: next }));
  };

  const save = async () => {
    if (server && (Object.keys(timeErrors).length || periodAction)) {
      setFormError(periodAction ? '请先完成或取消当前时段操作。' : '请修正时段结束时间后再保存。');
      return;
    }
    let values: any;
    try {
      await form.validateFields();
      values = form.getFieldsValue(true);
    } catch {
      return;
    }
    const specForm: SpecForm = { ...specToForm({ mode }), mode, service_basis: serviceBasis, tier_price_basis: tierPriceBasis, periods, ...values };
    const errors = validateSpecForm(specForm);
    setLocalErrors(errors);
    if (errors.length > 0) {
      setFormError('费率配置还不满足保存条件，请按下方提示修改后再保存。');
      return;
    }
    setSaving(true);
    setFormError('');
    try {
      const body = {
        name: values.name,
        remark: values.remark ?? '',
        spec: formToSpec(specForm),
        display: values.display || DEFAULT_DISPLAY,
        expected_version: editing?.version || 0,
      };
      if (editing) await apiPut(`/api/v1/admin/settings/pricing-templates/${editing.id}`, body);
      else await apiPost('/api/v1/admin/settings/pricing-templates', body);
      message.success('计费模板已保存');
      setOpen(false);
      await load();
    } catch (e: any) {
      setFormError(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const openApply = async (template: Template) => {
    setApplying(template);
    setApplyError('');
    setSelectedStation(null);
    setApplyDevice('');
    setStations([]);
    await searchStations('');
  };

  // 选中站点后读一次该站点的计费现状：既是为了告诉运营这条链现在到第几版，
  // 也是为了回填下发时的乐观锁版本号。
  const loadScope = async (stationId: number) => {
    try {
      const matrix = await apiGet<{ station_latest_version?: number; items: ScopeDevice[] }>(
        '/api/v1/admin/settings/device-pricing', { station_id: stationId });
      setStationLatestVersion(matrix.station_latest_version || 0);
      setScopeDevices(matrix.items || []);
    } catch (e: any) {
      setStationLatestVersion(0);
      setScopeDevices([]);
      setApplyError(e.message || '读取站点计费现状失败');
    }
  };

  const apply = async () => {
    if (!applying) return;
    if (!selectedStation) {
      setApplyError('请选择要应用到的站点');
      return;
    }
    const device = applyDevice.trim();
    // 锁比的是 (station_id， device_id) 这条链的最新版：留空设备编号就是整站链，
    // 填了设备编号就是那台设备自己的链。写死 0 只能给「从未定价过的范围」用，
    // 站点一旦有规则，界面就再也换不了费率。
    const expected = device
      ? (scopeDevices.find(d => d.device_id === device)?.own_latest_version || 0)
      : stationLatestVersion;
    setSaving(true);
    setApplyError('');
    try {
      const result = await apiPost<{ version: number }>(`/api/v1/admin/settings/pricing-templates/${applying.id}/apply`, {
        station_id: selectedStation,
        device_id: device,
        request_id: crypto.randomUUID(),
        expected_version: expected,
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

  const enableTemplate = (template: Template) => Modal.confirm({
    title: '启用计费模板',
    content: `启用「${template.name}」后可以重新应用到站点或设备。已应用站点的现行规则不受影响，会继续按原样计费。`,
    okText: '启用',
    onOk: async () => {
      await apiPost(`/api/v1/admin/settings/pricing-templates/${template.id}/enable`);
      message.success('已启用');
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

  const setPeriods = (next: PeriodForm[]) => form.setFieldValue('periods', next);
  const setTiers = (pi: number, next: TierForm[]) => {
    const current: PeriodForm[] = form.getFieldValue('periods') || [];
    const copy = current.map((p, i) => (i === pi ? { ...p, tiers: next } : p));
    setPeriods(copy);
  };

  const stepOne = (
    <>
      <Space align="start" wrap>
        <Form.Item name="name" label="模板名称" rules={[{ required: true, whitespace: true, max: 64 }]}>
          <Input maxLength={64} placeholder="如：二轮标准梯度价" style={{ width: 240 }} />
        </Form.Item>
        <Form.Item name="remark" label="模板备注" rules={[{ max: 255 }]}>
          <Input maxLength={255} placeholder="选填，说明适用范围" style={{ width: 300 }} />
        </Form.Item>
      </Space>
      <Form.Item name="mode" label="计费方式" rules={[{ required: true }]}>
        <Select
          style={{ width: '100%', maxWidth: 600 }}
          options={MODE_OPTIONS as never}
          onChange={(value: ChargeMode) => switchMode(value)}
        />
      </Form.Item>
      <Alert type="info" showIcon message={MODE_META[mode]?.hint} />
    </>
  );

  const renderPeriods = () => {
    const list = periods.length ? periods : [blankPeriod()];
    const hasTimeErrors = Object.keys(timeErrors).length > 0;
    const validCoverage = list.every((p, i) => Number.isInteger(p.end_minute)
      && p.end_minute > (i ? list[i - 1].end_minute : 0) && p.end_minute <= 1440)
      && list[list.length - 1].end_minute === 1440;
    return <>
      <div className="pricing-period-hint">直接填写时间，开始时间自动衔接，最后一段固定至 24:00。</div>
      {validCoverage && <div className="pricing-day-preview">
        <div className="pricing-day-track" role="img" aria-label={`全天时段覆盖：${list.map((p, i) => `${minuteToClock(i ? list[i - 1].end_minute : 0)}至${minuteToClock(p.end_minute)}`).join('、')}`}>
          {list.map((p, i) => <div key={i} className="pricing-day-segment" style={{ flex: p.end_minute - (i ? list[i - 1].end_minute : 0) }}>
            {p.end_minute - (i ? list[i - 1].end_minute : 0) >= 120 ? `第 ${i + 1} 段` : ''}
          </div>)}
        </div>
        <div className="pricing-day-axis"><span>00:00</span><span>12:00</span><span>24:00</span></div>
      </div>}
      <Form.List name="periods">{(fields, { add, remove }) => fields.map(field => {
        const pi = field.name;
        const period = list[pi];
        if (!period) return null;
        const startMinute = pi ? list[pi - 1].end_minute : 0;
        const isLast = pi === list.length - 1;
        const duration = period.end_minute - startMinute;
        const activeAction = periodAction?.key === field.key ? periodAction : null;
        const endError = timeErrors[field.key];
        const closeAction = () => { setPeriodAction(null); setSplitError(''); };
        const tiers = period.tiers || [];
        const canAppendTier = canInsertTier(tiers, tiers.length);
        const tierEditor = <div className="pricing-tier-editor">
          <div className="pricing-tier-heading"><strong>功率档位 · {tiers.length} 档</strong>
            <span>{serviceBasis === 'minute_power' ? '电费与服务费一起填写' : '填写各档电费单价'}</span></div>
          <div className="pricing-tier-scroll" role="region" aria-label={`第 ${pi + 1} 段功率档位`} tabIndex={0}>
            <table className="pricing-tier-table" aria-label={`第 ${pi + 1} 段档位费率`}>
              <thead><tr><th scope="col">档位 / 覆盖范围</th><th scope="col">功率上限（瓦）</th>
                <th scope="col">{legacyPricing ? '旧版填写值' : `电费（${tierUnits.electric}）`}</th>
                {legacyPricing && <th scope="col">实际电价（元/度）</th>}
                {serviceBasis === 'minute_power' && <th scope="col">服务费（{tierUnits.service}）</th>}
                <th scope="col">操作</th></tr></thead>
              <tbody>{tiers.map((tier, ti) => {
                const lower = ti ? Number(tiers[ti - 1]?.max_watts ?? 0) + 1 : 0;
                const prefix = `第 ${pi + 1} 段第 ${ti + 1} 档`;
                return <tr key={ti}>
                  <th scope="row"><span>第 {ti + 1} 档</span><small>{lower}–{tier.max_watts ?? '待填写'} 瓦</small></th>
                  <td><Form.Item name={[pi, 'tiers', ti, 'max_watts']} rules={[{ required: true, message: '请填写功率上限' }]}>
                    <InputNumber aria-label={`${prefix}上限`} disabled={legacyPricing} min={0} max={9990} step={100} precision={0} style={{ width: '100%' }} />
                  </Form.Item></td>
                  <td><Form.Item name={[pi, 'tiers', ti, 'electric_yuan']} rules={[{ required: true, message: '请填写电费单价' }]}>
                    <InputNumber aria-label={`${prefix}${legacyPricing ? '旧版填写值' : `电费（${tierUnits.electric}）`}`} disabled={legacyPricing} min={0} max={10000} step={0.01} precision={2} style={{ width: '100%' }} />
                  </Form.Item></td>
                  {legacyPricing && <td>{(Math.trunc(Math.round(Number(tier.electric_yuan) * 100) * Number(tier.max_watts) / 1000) / 100).toFixed(2)}</td>}
                  {serviceBasis === 'minute_power' && <td><Form.Item name={[pi, 'tiers', ti, 'service_yuan']} rules={[{ required: true, message: '请填写服务费单价' }]}>
                    <InputNumber aria-label={`${prefix}服务费（${tierUnits.service}）`} min={0} max={10000} step={0.01} precision={2} style={{ width: '100%' }} />
                  </Form.Item></td>}
                  <td><Button type="text" danger aria-label={`删除${prefix}`} disabled={legacyPricing || tiers.length <= 1}
                    onClick={() => setTiers(pi, removeTier(form.getFieldValue(['periods', pi, 'tiers']) || [], ti))}>删除</Button></td>
                </tr>;
              })}</tbody>
            </table>
          </div>
          <div className="pricing-tier-footer">
            <Button disabled={legacyPricing || !canAppendTier} onClick={() => {
              const current: TierForm[] = form.getFieldValue(['periods', pi, 'tiers']) || [];
              if (canInsertTier(current, current.length)) setTiers(pi, insertTier(current, current.length));
            }}>＋ 新增档位</Button>
            <span className="pricing-period-hint">{tiers.length >= 8 ? '已达到 8 档上限。' : !canAppendTier ? '请先填写有效的递增上限，并在末档之后留出新增范围。' : '新增档位追加至尾部，保留已有费率。只填上限，下限自动衔接。最多 8 档，最高 9990 瓦。'}</span>
          </div>
        </div>;
        return <div key={field.key} className="pricing-period-item">
          <div className={`pricing-period-row${energyBasis ? ' pricing-period-row-energy' : ''}`}>
            <strong className="pricing-period-number">第 {pi + 1} 段</strong>
            <Form.Item label="开始时间 · 自动"><Input aria-label={`第 ${pi + 1} 段开始时间`} readOnly value={minuteToClock(startMinute)} /></Form.Item>
            <Form.Item name={[pi, 'end_minute']} label={`结束时间${isLast ? ' · 固定' : ''}`}>
              <PeriodTimeInput periods={list} index={pi} fixed={isLast} onValidityChange={error => setTimeErrors(previous => {
                if (previous[field.key] === error) return previous;
                const next = { ...previous };
                if (error) next[field.key] = error; else delete next[field.key];
                return next;
              })} />
            </Form.Item>
            <div className="pricing-period-duration"><span>覆盖时长</span><div>{duration >= 0 ? `${Math.floor(duration / 60)} 小时 ${duration % 60} 分` : '时间无效'}</div></div>
            {energyBasis && <Form.Item name={[pi, 'electric_yuan']} label="电价（元/度）" rules={[{ required: true }]}>
              <InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: '100%' }} />
            </Form.Item>}
            <Space className="pricing-period-actions" wrap>
              <Popconfirm title={`拆分第 ${pi + 1} 段时段`}
                open={activeAction?.kind === 'split'}
                disabled={list.length >= 48 || duration < 2 || hasTimeErrors}
                okText="确认拆分" cancelText="取消"
                okButtonProps={{ disabled: hasTimeErrors || !!splitError }}
                description={<div style={{ maxWidth: 320 }}>
                  <label htmlFor={`period-split-${field.key}`}>拆分时间</label>
                  <div>
                    <Input id={`period-split-${field.key}`} aria-label="拆分时间" value={activeAction?.kind === 'split' ? activeAction.time : ''} placeholder="HH:mm" maxLength={5} style={{ width: 120 }} status={splitError ? 'error' : undefined}
                      onChange={e => {
                        if (activeAction?.kind !== 'split') return;
                        const time = e.target.value;
                        setPeriodAction({ ...activeAction, time });
                        try {
                          const minute = clockToMinute(time.trim());
                          if (minute === undefined) throw new Error('请输入有效时间，例如 08:00。');
                          splitPeriod(form.getFieldValue('periods'), pi, minute);
                          setSplitError('');
                        } catch (error) { setSplitError(error instanceof Error ? error.message : '拆分时间无效。'); }
                      }} />
                  </div>
                  <div className="pricing-period-hint">新时段复制本段全部费率，你可以继续修改。</div>
                  {splitError && <div className="pricing-time-error" role="alert">{splitError}</div>}
                </div>}
                onOpenChange={visible => {
                  if (visible) {
                    const minute = suggestedSplitMinute(form.getFieldValue('periods'), pi);
                    if (minute === undefined) return;
                    setPeriodAction({ key: field.key, kind: 'split', time: minuteToClock(minute) }); setSplitError('');
                  } else if (activeAction?.kind === 'split') closeAction();
                }}
                onConfirm={() => {
                  if (activeAction?.kind !== 'split' || hasTimeErrors || splitError) return;
                  const minute = clockToMinute(activeAction.time.trim());
                  try {
                    if (minute === undefined) throw new Error('请输入有效时间，例如 08:00。');
                    const next = splitPeriod(form.getFieldValue('periods'), pi, minute);
                    form.setFieldValue(['periods', pi, 'end_minute'], next[pi].end_minute);
                    add(next[pi + 1], pi + 1);
                    closeAction(); setLocalErrors([]); setFormError('');
                    requestAnimationFrame(() => form.scrollToField(['periods', pi + 1, 'end_minute'], { focus: true, block: 'nearest' }));
                  } catch (error) { setSplitError(error instanceof Error ? error.message : '拆分时间无效。'); }
                }}>
                <Button type="link" disabled={list.length >= 48 || duration < 2 || hasTimeErrors}>拆分此时段</Button>
              </Popconfirm>
              <Popconfirm title={`删除第 ${pi + 1} 段时段？`}
                description={isLast ? `删除后，第 ${pi} 段将延长至 24:00，使用第 ${pi} 段费率。`
                  : `删除后，第 ${pi + 2} 段将从 ${minuteToClock(startMinute)} 开始，使用第 ${pi + 2} 段费率。`}
                open={activeAction?.kind === 'delete'}
                disabled={list.length <= 1 || hasTimeErrors}
                okText="确认删除" cancelText="取消" okButtonProps={{ danger: true, disabled: hasTimeErrors }}
                onOpenChange={visible => {
                  if (visible) {
                    setPeriodAction({ key: field.key, kind: 'delete', time: '' }); setSplitError('');
                  } else if (activeAction?.kind === 'delete') closeAction();
                }}
                onConfirm={() => {
                  if (hasTimeErrors || list.length <= 1) return;
                  const next = removePeriod(form.getFieldValue('periods'), pi);
                  remove(pi);
                  form.setFieldValue(['periods', next.length - 1, 'end_minute'], next[next.length - 1].end_minute);
                  closeAction(); setLocalErrors([]); setFormError('');
                }}>
                <Button type="link" danger disabled={list.length <= 1 || hasTimeErrors}>删除时段</Button>
              </Popconfirm>
            </Space>
          </div>
          {!energyBasis && tierEditor}
          {endError && <span className="pricing-period-hint">覆盖预览仍显示上一次有效时间。</span>}
        </div>;
      })}</Form.List>
      <div className="pricing-period-hint" aria-live="polite">{hasTimeErrors ? '请修正结束时间后再保存。' : validCoverage ? `已完整覆盖全天 · ${list.length} 个时段` : '时段尚未完整覆盖全天。'}{list.length >= 48 ? ' 已达到 48 段上限。' : ''}</div>
    </>;
  };

  const preview = async () => {
    if (periodAction || Object.keys(timeErrors).length) {
      setFormError('请先完成当前时段操作并修正时间，再预览方案。'); return;
    }
    try {
      await form.validateFields(form.getFieldsError().map(field => field.name).filter(name => name[0] !== 'name' && name[0] !== 'remark'));
    } catch (error: any) {
      setLocalErrors((error.errorFields || []).flatMap((field: { errors: string[] }) => field.errors));
      setFormError('请完善填写内容后再预览，可返回对应步骤修改。'); return;
    }
    const values = form.getFieldsValue(true);
    const specForm: SpecForm = { ...specToForm({ mode }), ...values, mode, service_basis: serviceBasis, tier_price_basis: tierPriceBasis, periods };
    const errors = validateSpecForm(specForm);
    setLocalErrors(errors);
    if (errors.length) { setFormError('请完善费率配置后再预览。'); return; }
    setFormError('');
    setPreviewDraft({ name: values.name || '', spec: formToSpec(specForm), display: values.display || DEFAULT_DISPLAY });
  };

  const stepTwo = (
    <>
      {server ? <>
        {legacyPricing && <Alert type="warning" showIcon style={{ marginBottom: 12 }} message="这是旧版电价，请先转换后编辑"
          description="下表列出原填写值与实际等效电价。转换将沿用原实际收费水平，不按“元/小时”的旧文字重新定价。保存只更新模板，已应用的站点规则和历史订单保持原样。"
          action={<Button onClick={() => {
            try {
              form.setFieldsValue(convertLegacyPowerForm(form.getFieldsValue(true) as SpecForm));
              setFormError(''); setLocalErrors([]);
              message.success('已按原实际收费转换为元/度，请核对单价后保存');
            } catch (e: any) { setFormError(e.message || '旧版电价无法转换，请新建模板重新定价'); }
          }}>转换为元/度编辑</Button>} />}
        {!legacyPricing && editing && isLegacyPowerPricing(editing.spec) && <Alert type="info" showIcon style={{ marginBottom: 12 }}
          message="已按原实际收费转换为元/度。请核对单价后保存；站点现行规则保持原样，应用模板后才使用新版本。" />}
        <div className="pricing-billing-controls" role="region" aria-label="收费口径与服务费">
          <div className="pricing-billing-title"><strong>电费怎么收</strong><Tag color="blue">{mode === 'server_max_power' ? '按最高功率分档 · 元/小时' : energyBasis ? '统一电价 · 元/度' : '按实时功率分档 · 元/度'}</Tag></div>
          <div className="pricing-billing-explanation">{MODE_META[mode].hint}</div>
          <Divider style={{ margin: '12px 0' }} />
          <div className="pricing-billing-fields">
            <Form.Item name="service_basis" label="服务费口径" rules={[{ required: true }]}>
              <Select options={SERVICE_OPTIONS.filter(o => !(o.value === 'minute_power' && energyBasis)).map(o => ({ value: o.value, label: o.value === 'minute_power' ? `${o.label}（${tierUnits.service}）` : o.label }))} />
            </Form.Item>
            {serviceBasis === 'energy' && <Form.Item name="service_kwh_yuan" label="统一服务费（元/度）" rules={[{ required: true }]}>
              <InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: '100%' }} />
            </Form.Item>}
            {serviceBasis === 'minute' && <Form.Item name="service_minute_yuan" label="统一服务费（元/分钟）" rules={[{ required: true }]}>
              <InputNumber min={0} max={10000} step={0.01} precision={2} addonBefore="¥" style={{ width: '100%' }} />
            </Form.Item>}
            {serviceBasis === 'session' && <Form.Item name="service_session_yuan" label="每次充电服务费（元/次）" rules={[{ required: true }]}>
              <InputNumber min={0} max={10000} step={0.5} precision={2} addonBefore="¥" style={{ width: '100%' }} />
            </Form.Item>}
          </div>
          <div className="pricing-period-hint" aria-live="polite">{serviceBasis === 'minute_power'
            ? `服务费在下面的档位表中与电费一起填写，${mode === 'server_max_power' ? '按整场最高功率所在档位单价乘充电小时数' : '按各时段实时功率所在档位单价乘电量'}。切换口径会保留已填写的单价。`
            : serviceBasis === 'none' ? '仅收取电费。' : `此服务费单价适用于所有时段${energyBasis ? '' : '和档位'}，${serviceBasis === 'energy' ? '按电量结算' : serviceBasis === 'minute' ? '按充电分钟数结算' : '每次充电收取一次'}。`}</div>
        </div>
        <Divider orientation="left" plain>时段费率</Divider>
        {renderPeriods()}

      </> : <>
        <Alert type="info" showIcon style={{ marginBottom: 12 }}
          message="设备计费没有电价：费用在用户支付时已收取，设备按获准的时长/电量/功率自行执行。服务端不再计算金额，本模板不保存任何费率。" />
        <Alert type="warning" showIcon style={{ marginTop: 8 }}
          message="套餐按自己的价格结算，与费率无关，请在「模板 → 套餐模板」页签维护后单独上架。" />
      </>}

    </>
  );

  const stepConfig = <>
    {server ? <>
        <Collapse ghost style={{ marginTop: 8 }} defaultActiveKey={['extra']} items={[{ key: 'extra', label: '全局策略与封顶', children: <Space direction="vertical" style={{ width: '100%' }}>
          <Form.Item name="loss_percent" label="电损率（%）" extra="按此比例放大可计费电量，弥补线路损耗。">
            <InputNumber min={0} max={10} step={0.1} precision={2} addonAfter="%" style={{ width: 200 }} />
          </Form.Item>
          <Form.Item name="free_minutes" label="规定时间内免费充电（分钟）" extra="在此分钟内结束充电，本次不计费。0 表示不启用。">
            <InputNumber min={0} max={1440} style={{ width: 200 }} />
          </Form.Item>
          <Form.Item name="min_electric_yuan" label="电费最低消费（元）" extra="电费低于该金额时按该金额收取，仅针对电费，不含服务费。">
            <InputNumber min={0} max={10000} step={0.5} precision={2} addonBefore="¥" style={{ width: 200 }} />
          </Form.Item>
          <Form.Item name="spend_cap_yuan" label="单场费用封顶（元）" extra="服务端计费的订单达到该金额即停止，0 表示不封顶。">
            <InputNumber min={0} max={10000} step={1} precision={2} addonBefore="¥" style={{ width: 200 }} />
          </Form.Item>
          <Form.Item name="stop_grace_seconds" label="停机宽限（秒）" extra="通知设备停止后，等待多久仍视为订单未结束。">
            <InputNumber min={0} max={3600} style={{ width: 200 }} />
          </Form.Item>
          <Form.Item name="multiplier_on" label="按渠道给出电费倍率" valuePropName="checked" extra="10000 基点 = 1.0 倍，仅作用于电费，不作用于服务费。">
            <Switch />
          </Form.Item>
          {multiplierOn && <Space align="start" wrap>
            <Form.Item name="temp_bp" label="临时费率（基点）" rules={[{ required: true }]}><InputNumber min={0} max={100000} step={100} style={{ width: 200 }} /></Form.Item>
            <Form.Item name="card_bp" label="刷卡费率（基点）" rules={[{ required: true }]}><InputNumber min={0} max={100000} step={100} style={{ width: 200 }} /></Form.Item>
          </Space>}
        </Space> }]} />
    </> : <>
        {mode === 'device_duration' && <>
          <Divider orientation="left" plain>时长策略</Divider>
          <Form.Item name={['time_charge', 'stop_when_full']} label="充满后自动结束" valuePropName="checked"
            extra="关闭则一直充到时长用尽。"><Switch /></Form.Item>
          <Space align="start" wrap>
            <Form.Item name={['time_charge', 'max_minutes']} label="单场时长上限（分钟）" extra="0 表示不限制。">
              <InputNumber min={0} max={999} style={{ width: 200 }} />
            </Form.Item>
            <Form.Item name={['time_charge', 'float_power_deci_watts']} label="涓流功率（0.1 瓦）" extra="满电后的收尾阶段功率上限。">
              <InputNumber min={0} max={500} style={{ width: 200 }} />
            </Form.Item>
            <Form.Item name={['time_charge', 'float_seconds']} label="涓流阶段（秒）" extra="涓流阶段最长持续时间。">
              <InputNumber min={0} max={10800} style={{ width: 200 }} />
            </Form.Item>
          </Space>
        </>}
    </>}
      <Collapse ghost style={{ marginTop: 8 }} defaultActiveKey={['common']} items={[{ key: 'common', label: '刷卡与下单', children: <Space align="start" wrap>
        <Form.Item name="card_max_minutes" label="刷卡订单最长时长（分钟）" extra="0 表示不限制。固件上限 999 分钟。">
          <InputNumber min={0} max={999} style={{ width: 200 }} />
        </Form.Item>
        <Form.Item name="default_charge_way" label="默认下单方式" extra="选填，小程序发起充电时的默认选项。">
          <Input maxLength={32} placeholder="如：扫码 / 刷卡" style={{ width: 200 }} />
        </Form.Item>
      </Space> }]} />

  </>;

  const stepDisplay = (<>
      <Divider orientation="left" plain>用户界面展示</Divider>
      <div className="pricing-display-grid">
        <section className="pricing-display-group" aria-label="充电信息展示">
          <h4>充电信息</h4>
          {([
            ['show_energy', '显示充电电量'], ['show_power', '展示充电功率'],
            ['show_tariff', '显示时段计费详情'], ['show_method', '显示计费方式'], ['show_rule', '展示规则说明'],
          ] as const).map(([name, label]) => <Form.Item key={name} className="pricing-display-option" layout="horizontal" name={['display', name]} label={label} colon={false} valuePropName="checked">
            <Switch aria-label={label} />
          </Form.Item>)}
        </section>
        <section className="pricing-display-group" aria-label="费用与格式展示">
          <h4>费用与格式</h4>
          <Form.Item className="pricing-display-option" layout="horizontal" name={['display', 'show_fee_split']} label="订单详情显示电费与服务费" colon={false} valuePropName="checked">
            <Switch aria-label="订单详情显示电费与服务费" />
          </Form.Item>
          {showFeeSplit && <Form.Item className="pricing-display-option pricing-display-dependent" layout="horizontal" name={['display', 'fee_split_inline']} label="费用直接显示在支付金额后" colon={false} valuePropName="checked">
            <Switch aria-label="费用直接显示在支付金额后" />
          </Form.Item>}
          <Form.Item className="pricing-display-option" layout="horizontal" name={['display', 'show_fee_on_end']} label="结束充电推送显示费用" colon={false} valuePropName="checked">
            <Switch aria-label="结束充电推送显示费用" />
          </Form.Item>
          <Form.Item className="pricing-display-option" layout="horizontal" name={['display', 'hide_unit']} label="隐藏单位" colon={false} valuePropName="checked">
            <Switch aria-label="隐藏单位" />
          </Form.Item>
        </section>
      </div>
    </>
  );

  return <>
    <Space style={{ marginBottom: 12 }}>
      <Button onClick={() => void load()} loading={loading}>刷新</Button>
      {canCreate && <Button type="primary" onClick={() => void openEditor()}>新建计费模板</Button>}
    </Space>
    <Alert type="info" showIcon style={{ marginBottom: 12 }}
      message="计费模板描述计费口径与用户端展示；套餐在本页「套餐模板」页签维护。模板需要「应用到站点/设备」后才生效，修改模板不会改变已应用站点的现行计费。" />
    {listError && <LoadError title="计费模板列表加载失败" detail={listError} onRetry={() => void load()} />}
    <Table rowKey="id" dataSource={templates} loading={loading} scroll={{ x: 1000 }} columns={[
      { title: '名称', render: (_: unknown, r: Template) => <>{r.name}<div style={{ color: '#999' }}>v{r.version}{r.remark ? ` · ${r.remark}` : ''}</div></> },
      { title: '计费方式', render: (_: unknown, r: Template) => <Tag color={r.spec && isServerBilled(r.spec.mode) ? 'blue' : 'purple'}>{modeLabel(r.spec?.mode)}</Tag> },
      { title: '费率', render: (_: unknown, r: Template) => describeSpec(r.spec) },
      { title: '状态', render: (_: unknown, r: Template) => <>{r.status === 'active' && isLegacyPowerPricing(r.spec)
        ? <Tag color="orange">旧版 · 待转换</Tag> : <Tag color={r.status === 'active' ? 'green' : 'default'}>{r.status === 'active' ? '可应用' : '已停用'}</Tag>}</> },
      { title: '已应用站点', dataIndex: 'applied_stations', render: (v?: string) => v || <span style={{ color: '#999' }}>未应用</span> },
      {
        title: '操作', render: (_: unknown, r: Template) => <Space>
          <Button type="link" onClick={() => void view(r)}>查看</Button>
          {canCreate && r.status === 'active' && <Button type="link" disabled={isLegacyPowerPricing(r.spec)} title={isLegacyPowerPricing(r.spec) ? '请先编辑并转换旧版电价' : undefined} onClick={() => void openApply(r)}>应用</Button>}
          {canUpdate && <Button type="link" onClick={() => void openEditor(r)}>编辑</Button>}
          {canCreate && <Button type="link" disabled={isLegacyPowerPricing(r.spec)} title={isLegacyPowerPricing(r.spec) ? '请先编辑并转换旧版电价' : undefined} onClick={() => copyTemplate(r)}>复制</Button>}
          {canUpdate && r.status === 'active' && <Button type="link" danger onClick={() => disableTemplate(r)}>停用</Button>}
          {canUpdate && r.status !== 'active' && <Button type="link" onClick={() => enableTemplate(r)}>启用</Button>}
        </Space>,
      },
    ]} />

    <Modal
      title={editing ? `编辑计费模板 · ${editing.name}` : '新建计费模板'}
      open={open} width={1080} onCancel={() => setOpen(false)}
      confirmLoading={saving}
      footer={[
        <Button key="cancel" onClick={() => setOpen(false)}>取消</Button>,
        step > 0 && <Button key="back" onClick={() => { setLocalErrors([]); setFormError(''); setPeriodAction(null); setSplitError(''); setStep(step - 1); }}>上一步</Button>,
        <Button key="preview" disabled={saving} onClick={() => void preview()}>预览</Button>,
        <Button key="next" type="primary" loading={saving} disabled={step === 1 && server && (legacyPricing || Object.keys(timeErrors).length > 0 || !!periodAction)} onClick={async () => {
          if (step === 0) {
            try {
              await form.validateFields(['name', 'mode']);
            } catch {
              return;
            }
            setStep(1);
            return;
          }
          if (step < 3) {
            const rateFields = ['periods', 'service_basis', 'service_kwh_yuan', 'service_minute_yuan', 'service_session_yuan', 'tier_price_basis'];
            try { await form.validateFields(form.getFieldsError().map(field => field.name).filter(name => step === 1 ? rateFields.includes(String(name[0])) : !rateFields.includes(String(name[0])) && name[0] !== 'name' && name[0] !== 'remark')); } catch { return; }
            const values = form.getFieldsValue(true);
            const current = step === 1 ? Object.fromEntries(rateFields.map(key => [key, values[key]])) : values;
            const errors = validateSpecForm({ ...specToForm({ mode }), ...current, mode, service_basis: serviceBasis, tier_price_basis: tierPriceBasis, periods });
            setLocalErrors(errors);
            if (errors.length) { setFormError('请修正配置后继续。'); return; }
            setFormError(''); setStep(step + 1); return;
          }
          await save();
        }}>{step === 3 ? '保存' : '下一步'}</Button>,
      ]}
      destroyOnHidden
    >
      {formError && <Alert type="error" showIcon message={formError} style={{ marginBottom: 12 }}
        description={localErrors.length ? <ul style={{ margin: 0, paddingLeft: 18 }}>{localErrors.map(e => <li key={e}>{e}</li>)}</ul> : undefined} />}
      <Steps current={step} size="small" style={{ marginBottom: 16 }} items={[{ title: '基本信息' }, { title: '费率' }, { title: '配置' }, { title: '用户界面展示' }]} />
      <Form form={form} name="pricing_template" layout="vertical">
        <div hidden={step !== 0}>{stepOne}</div>
        <div hidden={step !== 1}>{stepTwo}</div>
        <div hidden={step !== 2}>{stepConfig}</div>
        <div hidden={step !== 3}>{stepDisplay}</div>
      </Form>
    </Modal>

    {previewDraft && <PricingPreview draft={previewDraft} onClose={() => setPreviewDraft(null)} />}

    <Modal title={viewing ? `计费模板详情 · ${viewing.name}` : '计费模板详情'} open={!!viewing} width={860}
      onCancel={() => setViewing(null)} footer={<Button onClick={() => setViewing(null)}>关闭</Button>}>
      {viewError ? (
        <LoadError title="计费模板详情加载失败" detail={viewError}
          onRetry={() => { if (viewing) void view(viewing); }} />
      ) : viewLoading ? <Spin /> : viewing && <>
        <Descriptions size="small" column={2} bordered items={[
          { key: 'name', label: '名称', children: viewing.name },
          { key: 'status', label: '状态', children: viewing.status === 'active' ? '可应用' : '已停用' },
          { key: 'remark', label: '备注', children: viewing.remark || '—', span: 2 },
          { key: 'version', label: '版本', children: `v${viewing.version}` },
          { key: 'mode', label: '计费方式', children: modeLabel(viewing.spec?.mode) },
        ]} />
        <Divider orientation="left" plain>费率</Divider>
        <div>{describeSpec(viewing.spec)}</div>
        {isLegacyPowerPricing(viewing.spec) && <Alert type="info" showIcon style={{ marginTop: 8 }} message="下方电价是按旧版实际计算结果展示的等效元/度单价，原模板未改动。编辑转换后才可再次应用。" />}
        {viewing.spec?.electric?.periods?.map((p, i, all) => <div key={i} style={{ marginTop: 8 }}>
          {/* 时段是链式的，开始时刻由上一段的结束时刻推导，不是都从 00:00 起 */}
          <strong>时段 {i + 1}：</strong>{minuteToClock(i > 0 ? (all[i - 1]?.end_minute ?? 0) : 0)} → {minuteToClock(p.end_minute)}
          {p.tiers?.length
            ? p.tiers.map((t, j) => <div key={j} style={{ paddingLeft: 16, color: '#666' }}>
              第 {j + 1} 档：{j > 0 ? `${p.tiers![j - 1].max_watts + 1}–` : '0–'}{t.max_watts} 瓦 · 电费 ¥{(effectiveTierElectricCents(viewing.spec!, t) / 100).toFixed(2)} / {tierRateUnits(viewing.spec!.mode).electric.replace('元/', '')}{viewing.spec?.service?.basis === 'minute_power' ? ` · 服务费 ¥${((t.service_cents || 0) / 100).toFixed(2)} / ${tierRateUnits(viewing.spec.mode).service.replace('元/', '')}` : ''}
            </div>)
            : <div style={{ paddingLeft: 16, color: '#666' }}>电价 ¥{((p.electric_cents || 0) / 100).toFixed(2)}/度</div>}
        </div>)}
        {viewing.spec && <div style={{ marginTop: 8, color: '#666' }}>
          免费时长：{viewing.spec.free_minutes || 0} 分钟 · 电费最低消费：¥{((viewing.spec.min_electric_cents || 0) / 100).toFixed(2)} · 电损率：{((viewing.spec.loss_rate_bp || 0) / 100).toFixed(2)}%
        </div>}
        <Divider orientation="left" plain>用户端展示</Divider>
        <div>{describeDisplay(viewing.display)}</div>
      </>}
    </Modal>

    <Modal title={applying ? `将「${applying.name}」应用为计费规则` : '应用'} open={!!applying}
      onCancel={() => setApplying(null)} onOk={() => void apply()} confirmLoading={saving} okText="应用"
      okButtonProps={{ disabled: !selectedStation }}>
      {applyError && <Alert type="error" showIcon message={applyError} style={{ marginBottom: 12 }} />}
      {stationsError && <LoadError title="运营中站点列表加载失败" detail={stationsError} onRetry={() => void searchStations('')} />}
      <Alert type="warning" showIcon style={{ marginBottom: 12 }}
        message="应用后该范围按此模板计费并生成新版本；套餐可从「模板 → 套餐模板」页签或站点工作区单独上架。" />
      <Space direction="vertical" style={{ width: '100%' }}>
        <Select showSearch allowClear aria-label="选择站点" placeholder="输入站点名称或地址进行筛选"
          value={selectedStation ?? undefined} loading={stationsLoading} filterOption={false}
          onSearch={value => void searchStations(value)}
          onChange={value => {
            setSelectedStation(value ?? null);
            setStationLatestVersion(0);
            setScopeDevices([]);
            if (value != null) void loadScope(value);
          }}
          options={stations.map(s => ({ value: s.id, label: s.name }))} style={{ width: '100%' }} />
        <Input aria-label="设备编号" placeholder="设备编号（留空表示发布为站点默认规则）" maxLength={64}
          value={applyDevice} onChange={e => setApplyDevice(e.target.value)} />
      </Space>
      {selectedStation != null && (
        <div style={{ marginTop: 8, color: '#999' }}>
          {(() => {
            const device = applyDevice.trim();
            const known = device ? scopeDevices.find(d => d.device_id === device) : undefined;
            const current = device ? (known?.own_latest_version ?? 0) : stationLatestVersion;
            return device && !known
              ? `设备 ${device} 不在该站点设备列表中，将由服务端校验`
              : `将覆盖 ${device ? `设备 ${device}` : '整站默认'} 当前规则链（现为 v${current}），下发后成为 v${current + 1}`;
          })()}
        </div>
      )}
      {stations.length === 0 && !stationsLoading && !stationsError && <div style={{ marginTop: 8, color: '#999' }}>没有匹配的运营中站点</div>}
    </Modal>
  </>;
}
