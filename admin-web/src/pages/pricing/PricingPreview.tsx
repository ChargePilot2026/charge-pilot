import { TABLE_PAGINATION } from '../../utils/tablePagination';
import { useEffect, useState } from 'react';
import { Alert, Button, Collapse, Descriptions, Input, InputNumber, Modal, Select, Space, Spin, Table, Tag } from 'antd';
import { apiPost } from '../../api/client';
import { clockToMinute, describeDisplay, isServerBilled, minuteToClock, modeLabel, tierRateUnits, type Display, type Spec } from './model';

type Segment = { minutes: number; watts: number };
type Scenario = { name: string; start_minute: number; channel: 'temp' | 'card'; segments: Segment[] };
type Fee = { electric_cents: number; service_cents: number; total_cents: number; billable_wh: number };
type Result = Scenario & { fee: Fee; base_fee: Fee; before_minimum: Fee; energy_wh: number; minutes: number; cap_reached: boolean; card_limit_exceeded: boolean };
export type PreviewDraft = { name: string; spec: Spec; display: Display };
const money = (cents = 0) => (cents / 100).toFixed(2);
const factor = (spec: Spec, channel: Scenario['channel']) => ((channel === 'card' ? spec.multiplier?.card_bp : spec.multiplier?.temp_bp) || 10000) / 10000;

function scenariosFor(spec: Spec): Scenario[] {
  const scenarios: Scenario[] = [];
  let start = 0;
  const periods = spec.electric?.periods || [];
  periods.forEach((period, pi) => {
    const watts = period.tiers?.map((tier, ti) => Math.min(tier.max_watts, Math.max(ti ? period.tiers![ti - 1].max_watts + 1 : 1, 300))) || [300];
    watts.forEach((power, ti) => scenarios.push({ name: `第 ${pi + 1} 时段${period.tiers ? ` · 第 ${ti + 1} 档` : ''}`, start_minute: start, channel: 'temp', segments: [{ minutes: Math.min(60, period.end_minute - start), watts: power }] }));
    start = period.end_minute;
  });
  const first = scenarios.find(s => s.segments[0].watts > 0) || scenarios[0];
  if (first) {
    scenarios.push({ ...first, name: '扫码 · 充电 2 小时', segments: [{ minutes: 120, watts: first.segments[0].watts }] });
    scenarios.push({ ...first, name: '刷卡 · 相同用量对比', channel: 'card', segments: [{ minutes: 120, watts: first.segments[0].watts }] });
    const tiers = periods[0]?.tiers;
    if (tiers && tiers.length > 1) scenarios.push({ ...first, name: '功率变化 · 低档到高档', segments: [{ minutes: 30, watts: first.segments[0].watts }, { minutes: 30, watts: Math.min(tiers[tiers.length - 1].max_watts, Math.max(tiers[tiers.length - 2].max_watts + 1, 600)) }] });
    periods.slice(0, -1).forEach((period, pi) => scenarios.push({ ...first, name: `跨第 ${pi + 1} / ${pi + 2} 时段`, start_minute: Math.max(0, period.end_minute - 15), segments: [{ minutes: 30, watts: first.segments[0].watts }] }));
    scenarios.push({ ...first, name: '跨午夜 · 23:45 开始', start_minute: 1425, segments: [{ minutes: 30, watts: first.segments[0].watts }] });
    if (spec.free_minutes) {
      scenarios.push({ ...first, name: '恰好在免费时长内结束', segments: [{ minutes: spec.free_minutes, watts: first.segments[0].watts }] });
      if (spec.free_minutes < 1440) scenarios.push({ ...first, name: '超过免费时长 1 分钟', segments: [{ minutes: spec.free_minutes + 1, watts: first.segments[0].watts }] });
    }
    if (spec.min_electric_cents) scenarios.push({ ...first, name: '短充 · 检查最低电费', segments: [{ minutes: Math.min(1440, (spec.free_minutes || 0) + 1), watts: first.segments[0].watts }] });
    if (spec.spend_cap_cents) scenarios.push({ ...first, name: '长充 · 检查费用停止阈值', segments: [{ minutes: 1440, watts: first.segments[0].watts }] });
    if (spec.card_max_minutes) scenarios.push({ ...first, name: '超过刷卡时长上限 1 分钟', channel: 'card', segments: [{ minutes: spec.card_max_minutes + 1, watts: first.segments[0].watts }] });
    scenarios.push({ ...first, name: '没有用电 · 不收费', segments: [{ minutes: 60, watts: 0 }] });
  }
  return scenarios;
}

function calculationRows(spec: Spec, result: Result) {
  const periods = spec.electric!.periods;
  const peak = Math.max(...result.segments.map(s => s.watts));
  const groups = new Map<string, { period: number; tier: number; watts: number; minutes: number; wh: number }>();
  let minute = result.start_minute;
  result.segments.forEach(segment => {
    const wh = Math.round(segment.watts * segment.minutes / 60);
    for (let i = 0; i < segment.minutes; i++, minute++) {
      const pi = periods.findIndex(p => p.end_minute > ((spec.mode === 'server_max_power' ? result.start_minute : minute) % 1440));
      const watts = spec.mode === 'server_max_power' ? peak : segment.watts;
      const tiers = periods[pi].tiers;
      const found = tiers?.findIndex(t => watts <= t.max_watts) ?? -1;
      const ti = tiers ? (found < 0 ? tiers.length - 1 : found) : -1;
      const key = `${pi}-${ti}-${watts}`;
      const row = groups.get(key) || { period: pi, tier: ti, watts, minutes: 0, wh: 0 };
      row.minutes++;
      row.wh += Math.floor(wh / segment.minutes) + (i < wh % segment.minutes ? 1 : 0);
      groups.set(key, row);
    }
  });
  return [...groups.values()].map(row => {
    const period = periods[row.period];
    const tier = period.tiers?.[row.tier];
    const kwh = row.wh / 1000 * (1 + (spec.loss_rate_bp || 0) / 10000);
    const units = spec.mode === 'server_max_power' ? `${row.minutes}/60 小时` : `${kwh.toFixed(6)} 度`;
    const service = spec.service;
    const serviceFormula = !service ? '不收服务费' : service.basis === 'minute_power'
      ? `${units} × ${money(tier?.service_cents)} ${tierRateUnits(spec.mode).service}`
      : service.basis === 'energy' ? `${kwh.toFixed(6)} 度 × ${money(service.cents_per_kwh)} 元/度`
      : service.basis === 'minute' ? `${row.minutes} 分钟 × ${money(service.cents_per_minute)} 元/分钟`
      : `整单收一次 ${money(service.cents_per_session)} 元（不按行重复收取）`;
    return { ...row, key: `${row.period}-${row.tier}-${row.watts}`, electric: `${units} × ${money(tier?.electric_cents ?? period.electric_cents)} ${tierRateUnits(spec.mode).electric}`, service: serviceFormula };
  });
}

export default function PricingPreview({ draft, onClose }: { draft: PreviewDraft; onClose: () => void }) {
  const { spec, display } = draft;
  const [results, setResults] = useState<Result[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [custom, setCustom] = useState<Result | null>(null);
  const [customLoading, setCustomLoading] = useState(false);
  const [customError, setCustomError] = useState('');
  const [time, setTime] = useState('08:00');
  const [minutes, setMinutes] = useState<number | null>(60);
  const [watts, setWatts] = useState<number | null>(300);
  const [channel, setChannel] = useState<Scenario['channel']>('temp');
  const server = isServerBilled(spec.mode);
  const load = async () => {
    setLoading(true); setError('');
    try { setResults((await apiPost<{ items: Result[] }>('/api/v1/admin/settings/pricing-templates/preview', { spec, scenarios: scenariosFor(spec) })).items); }
    catch (e) { setError(e instanceof Error ? e.message : '预览计算失败'); }
    finally { setLoading(false); }
  };
  useEffect(() => { if (server) void load(); }, [draft]);
  const customPreview = async () => {
    const start = clockToMinute(time);
    if (start === undefined || start >= 1440 || minutes == null || !Number.isInteger(minutes) || minutes < 1 || minutes > 1440 || watts == null || !Number.isInteger(watts) || watts < 0 || watts > 100000) {
      setCustomError('请填写有效的开始时间（00:00–23:59）、时长（1–1440 分钟）和功率（0–100000 瓦）。'); return;
    }
    setCustomLoading(true); setCustomError(''); setCustom(null);
    try { setCustom((await apiPost<{ items: Result[] }>('/api/v1/admin/settings/pricing-templates/preview', { spec, scenarios: [{ name: '自定义场景', start_minute: start, channel, segments: [{ minutes, watts }] }] })).items[0]); }
    catch (e) { setCustomError(e instanceof Error ? e.message : '预览计算失败'); }
    finally { setCustomLoading(false); }
  };
  const detail = (result: Result) => <Space direction="vertical" style={{ width: '100%' }}>
    <div>开始：{minuteToClock(result.start_minute)}；持续 {result.minutes} 分钟；{result.channel === 'card' ? '刷卡' : '扫码'}；实测电量 {(result.energy_wh / 1000).toFixed(3)} 度；含电损计费电量 {(result.fee.billable_wh / 1000).toFixed(3)} 度。</div>
    <div>功率过程：{result.segments.map(s => `${s.watts} 瓦 × ${s.minutes} 分钟`).join(' → ')}。</div>
    {spec.mode === 'server_max_power' && <div>按整场最高功率 {Math.max(...result.segments.map(s => s.watts))} 瓦选档，整场使用开始时段的费率。</div>}
    <Table size="middle" pagination={TABLE_PAGINATION} scroll={{ x: 640 }} dataSource={calculationRows(spec, result)} columns={[
      { title: '采用费率', render: (_, r) => `第 ${r.period + 1} 时段${r.tier >= 0 ? ` · 第 ${r.tier + 1} 档` : ''}` },
      { title: '电费计算（倍率前）', dataIndex: 'electric' }, { title: '服务费计算', dataIndex: 'service' },
    ]} />
    <div>电费先累加，再乘{result.channel === 'card' ? '刷卡' : '扫码'}倍率 {factor(spec, result.channel)}，四舍五入到分；服务费独立累加并四舍五入到分，服务费不乘倍率。最低电费为 {money(spec.min_electric_cents)} 元。</div>
    {result.energy_wh === 0 ? <Alert type="info" message="全程没有用电，整单费用为 0，最低电费和按次服务费均不收取。" />
      : spec.free_minutes && result.minutes <= spec.free_minutes ? <Alert type="info" message={`在 ${spec.free_minutes} 分钟免费时长内结束，整单免单。上表仅解释费率，实际不收取。`} />
      : <div>含电损、未乘倍率及未补最低电费：电费 {money(result.base_fee.electric_cents)} 元，服务费 {money(result.base_fee.service_cents)} 元。{spec.free_minutes ? '超过免费时长后按整场用量计费，不扣除免费分钟。' : ''}</div>}
    {result.fee.electric_cents > result.before_minimum.electric_cents && <div>倍率后电费 {money(result.before_minimum.electric_cents)} 元低于最低电费，补收 {money(result.fee.electric_cents - result.before_minimum.electric_cents)} 元，电费合计 {money(result.fee.electric_cents)} 元。</div>}
    <strong>电费 {money(result.fee.electric_cents)} 元 + 服务费 {money(result.fee.service_cents)} 元 = 总费用 {money(result.fee.total_cents)} 元</strong>
    {result.cap_reached && <Alert type="warning" message={`该场景费用达到 ${money(spec.spend_cap_cents)} 元停止阈值，真实运行中会触发停止；上面的金额为假设完整用量的计费结果，不能视为实际订单应付金额。`} />}
    {result.card_limit_exceeded && <Alert type="warning" message={`该场景超过刷卡最长 ${spec.card_max_minutes} 分钟，实际订单受时长限制；上方为假设完整用量的计算。`} />}
  </Space>;
  return <Modal title={`计费方案预览 · ${draft.name || '未命名模板'}`} open width={1080} style={{ top: 32 }} styles={{ body: { maxHeight: 'calc(100vh - 180px)', overflowY: 'auto', paddingRight: 8 } }} onCancel={onClose} footer={<Button onClick={onClose}>返回修改</Button>}>
    <Alert type="info" showIcon message="基于当前未保存的填写内容生成，仅用于核对方案，不会保存或应用模板。" description={server ? '服务端计费金额调用正式计价引擎；示例按恒定功率估算实测电量并取整到瓦时，真实账单以设备计量、停止时机及订单规则为准。' : '当前模式由设备执行充电额度；本页展示执行策略和用户展示设置，支付金额不在计费模板中配置。'} />
    <Descriptions bordered size="small" column={2} style={{ marginTop: 16 }} items={[
      { key: 'mode', label: '计费方式', children: modeLabel(spec.mode), span: 2 },
      { key: 'service', label: '服务费', children: !spec.service ? '不收取' : spec.service.basis === 'minute_power' ? '随功率档位，见下方费率表' : spec.service.basis === 'energy' ? `${money(spec.service.cents_per_kwh)} 元/度` : spec.service.basis === 'minute' ? `${money(spec.service.cents_per_minute)} 元/分钟` : `${money(spec.service.cents_per_session)} 元/次` },
      { key: 'loss', label: '电损率', children: `${(spec.loss_rate_bp || 0) / 100}%（仅放大按电量计费部分）` },
      { key: 'free', label: '免费时长', children: spec.free_minutes ? `${spec.free_minutes} 分钟内整单免费；超过后整场计费` : '不启用' },
      { key: 'min', label: '最低电费', children: `${money(spec.min_electric_cents)} 元（仅补电费）` },
      { key: 'factor', label: '电费倍率', children: `扫码 ${factor(spec, 'temp')} 倍 / 刷卡 ${factor(spec, 'card')} 倍；服务费不变` },
      { key: 'cap', label: '费用停止阈值', children: spec.spend_cap_cents ? `${money(spec.spend_cap_cents)} 元；触发停止，不直接截断账单金额` : '不启用' },
      { key: 'grace', label: '停止等待', children: spec.stop_grace_seconds ? `${spec.stop_grace_seconds} 秒；不参与金额计算` : '使用系统默认值' },
      { key: 'card', label: '刷卡最长时长', children: spec.card_max_minutes ? `${spec.card_max_minutes} 分钟` : '不限制' },
      { key: 'way', label: '默认下单方式', children: spec.default_charge_way || '未设置' },
      { key: 'display', label: '用户界面展示', children: describeDisplay(display), span: 2 },
    ].filter(item => server || ['mode', 'card', 'way', 'display'].includes(item.key))} />
    {!server ? <>
      <Alert style={{ marginTop: 16 }} type="warning" message="设备计费模板不提供金额：支付价格在套餐或购买选项中确定，无法仅凭当前模板推算应付金额。" />
      {spec.time_charge && <Descriptions bordered size="small" style={{ marginTop: 16 }} items={[
        { key: 'full', label: '充满自动结束', children: spec.time_charge.stop_when_full ? '开启' : '关闭' },
        { key: 'max', label: '单场时长上限', children: spec.time_charge.max_minutes ? `${spec.time_charge.max_minutes} 分钟` : '不限制' },
        { key: 'float', label: '涓流策略', children: `${(spec.time_charge.float_power_deci_watts || 0) / 10} 瓦，最长 ${spec.time_charge.float_seconds || 0} 秒` },
      ]} />}
    </> : <>
      <Collapse style={{ marginTop: 16 }} items={[{ key: 'rates', label: '完整时段与功率档位费率', children: <Table size="middle" pagination={false} dataSource={spec.electric!.periods.flatMap((period, pi, all) => (period.tiers || [null]).map((tier, ti) => ({ key: `${pi}-${ti}`, time: `${minuteToClock(pi ? all[pi - 1].end_minute : 0)}–${minuteToClock(period.end_minute)}`, power: tier ? `${ti ? period.tiers![ti - 1].max_watts + 1 : 0}–${tier.max_watts} 瓦${ti === period.tiers!.length - 1 ? '（超出仍按末档）' : ''}` : '不分档', electric: `${money(tier?.electric_cents ?? period.electric_cents)} ${tierRateUnits(spec.mode).electric}`, service: spec.service?.basis === 'minute_power' ? `${money(tier?.service_cents)} ${tierRateUnits(spec.mode).service}` : '统一口径，见上方' })))} columns={[{ title: '时段', dataIndex: 'time' }, { title: '功率范围', dataIndex: 'power' }, { title: '电费单价', dataIndex: 'electric' }, { title: '服务费', dataIndex: 'service' }]} /> }]} />
      <h4>自定义充电场景</h4>
      <Space wrap>
        <label>开始时间 <Input aria-label="预览开始时间" value={time} onChange={e => { setTime(e.target.value); setCustom(null); }} style={{ width: 100 }} /></label>
        <label>时长 <InputNumber aria-label="预览充电分钟" min={1} max={1440} precision={0} value={minutes} onChange={value => { setMinutes(value); setCustom(null); }} addonAfter="分钟" /></label>
        <label>功率 <InputNumber aria-label="预览充电功率" min={0} max={100000} precision={0} value={watts} onChange={value => { setWatts(value); setCustom(null); }} addonAfter="瓦" /></label>
        <Select aria-label="预览启动方式" value={channel} onChange={value => { setChannel(value); setCustom(null); }} options={[{ value: 'temp', label: '扫码' }, { value: 'card', label: '刷卡' }]} />
        <Button loading={customLoading} onClick={() => void customPreview()}>计算场景</Button>
      </Space>
      {customError && <Alert type="error" message={customError} style={{ marginTop: 8 }} />}
      {custom && <div style={{ marginTop: 12 }}>{detail(custom)}</div>}
      <h4>按当前方案自动生成的示例</h4>
      {error ? <Alert type="error" message={error} action={<Button onClick={() => void load()}>重试</Button>} /> : loading ? <Spin /> : <Collapse items={results.map((result, i) => ({ key: String(i), label: <Space wrap><span>{result.name}</span><span>{minuteToClock(result.start_minute)} · {result.minutes} 分钟</span><strong>电费 {money(result.fee.electric_cents)} + 服务费 {money(result.fee.service_cents)} = {money(result.fee.total_cents)} 元</strong>{(result.cap_reached || result.card_limit_exceeded) && <Tag color="orange">超出运行限制</Tag>}</Space>, children: detail(result) }))} />}
    </>}
  </Modal>;
}
