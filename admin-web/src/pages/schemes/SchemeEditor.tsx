import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Checkbox, ConfigProvider, Drawer, Empty, Form, Input, InputNumber, Popconfirm, Segmented, Select, Space, Steps, Switch, Table, Typography, theme } from 'antd';
import { apiPost } from '../../api/client';
import { Algorithm, AmountDrafts, PackageMode, Scheme, clock, displayLabels, validateScheme, modes, money, priceEnergy, switchAmountAlgorithm } from './model';
import PeriodTimeInput from '../pricing/PeriodTimeInput';

export default function SchemeEditor({ value: initial, onSave, saving = false }: { value: Scheme; onSave: (s: Scheme) => Promise<void>; saving?: boolean }) {
  const { token } = theme.useToken();
  const [scheme, setScheme] = useState(initial);
  const [amountDrafts, setAmountDrafts] = useState<AmountDrafts>({});
  const timeDrafts = useRef<Partial<Record<Algorithm, Record<number, string>>>>({});
  const [step, setStep] = useState(0);
  const [durationEnabled, setDurationEnabled] = useState(initial.packages.some(p => p.mode === 'duration'));
  const stepIDs = [0, ...(scheme.amount ? [1] : []), ...(scheme.energy ? [2] : []), ...(durationEnabled ? [3] : []), 4];
  const stepIndex = stepIDs.indexOf(step);
  const [error, setError] = useState('');
  const [validationAttempt, setValidationAttempt] = useState(0);
  const feedbackRef = useRef<HTMLDivElement>(null);
  const [preview, setPreview] = useState(false);
  const [scenario, setScenario] = useState('ordinary');
  const [packageID, setPackageID] = useState<number>();
  const [result, setResult] = useState<any>();
  const [walletCents,setWalletCents]=useState(1000);
  const [previewing, setPreviewing] = useState(false);
  const [process, setProcess] = useState<any[]>([]);
  const [timeErrors, setTimeErrors] = useState<Record<number, string>>({});
  const issues = [...validateScheme(scheme), ...(durationEnabled && !scheme.packages.some(p => p.mode === 'duration') ? [{ message: '时长套餐', step: 3, fields: ['duration.packages'] }] : []), ...(!scheme.amount && !scheme.energy && !durationEnabled ? [{ message: '至少启用一个充电模式', step: 0, fields: ['modes'] }] : []), ...Object.entries(timeErrors).map(([i, message]) => ({ message: `第 ${Number(i) + 1} 段结束时间：${message}`, step: 1, fields: [`amount.periods.${i}.end_minute`] }))];
  const invalidFields = new Set(validationAttempt ? issues.flatMap(issue => issue.fields) : []);
  const invalid = (field: string) => invalidFields.has(field);
  const fieldProps = (field: string) => ({ status: invalid(field) ? 'error' as const : undefined, 'aria-invalid': invalid(field) });
  const feedback = validationAttempt && issues.length ? `请检查：${issues.map(issue => issue.message).join('、')}` : error;
  useEffect(() => { if (validationAttempt) feedbackRef.current?.scrollIntoView({ behavior: 'smooth', block: 'nearest' }); }, [validationAttempt]);
  const validate = () => { setValidationAttempt(n => n + 1); setError(''); return issues.length === 0; };
  const change = (s: Scheme) => { setScheme(priceEnergy(s)); setError(''); setResult(undefined); };
  const patch = (partial: Partial<Scheme>) => {
    if (partial.amount && (!scheme.amount || (partial.amount.algorithm === scheme.amount.algorithm && partial.amount.periods.length !== scheme.amount.periods.length))) {
      delete timeDrafts.current[partial.amount.algorithm];
    }
    change({ ...scheme, ...partial });
  };
  const selectAlgorithm = (algorithm: Algorithm) => {
    if (!scheme.amount || algorithm === scheme.amount.algorithm) return;
    const next = switchAmountAlgorithm(scheme.amount, amountDrafts, algorithm);
    setAmountDrafts(next.drafts);
    setTimeErrors({});
    patch({ amount: next.amount });
  };
  const updatePeriod = (i: number, p: any) => patch({ amount: { ...scheme.amount!, periods: scheme.amount!.periods.map((v, j) => j === i ? p : v) } });
  const rateInput = (field: string, value: number | undefined, onChange: (v: number) => void) => <InputNumber {...fieldProps(field)} aria-label="费率（元）" min={0} precision={2} value={Number.isFinite(value) ? value! / 100 : null} onChange={v => onChange(v == null ? NaN : Math.round(v * 100))} addonAfter={scheme.amount?.algorithm === 'server_energy' ? '元/度' : '元/小时'} />;
  const save = async () => { if (!validate()) return; try { await onSave(scheme); } catch (e: any) { setError(e.message || '保存失败'); } };
  const runPreview = async () => {
    if (!validate()) return;
    const selected = scheme.packages.find(p => p.id === packageID);
    if (!selected) { setError('请选择预览套餐'); return; }
    setPreviewing(true); setError(''); setResult(undefined);
    try {
      const isCard=scenario.startsWith('card_');
      const start = new Date(scenario==='cross_period'?'2026-09-30T09:30:00+08:00':'2026-09-30T00:00:00+08:00');
      let minutes = scenario === 'duration_limit' || scenario==='budget' ? scheme.policy.max_minutes : scenario === 'free' ? scheme.policy.free_minutes : scenario === 'free_over' ? scheme.policy.free_minutes+1 : scenario === 'early' ? 61 : isCard ? 150 : scenario==='minimum'?15:60;
      const end=new Date(+start+minutes*60000+(scenario==='early'?30000:0));
      const points=new Set([+start,+end]);
      if(scenario==='budget')for(let minute=10;minute<minutes;minute+=10)points.add(+start+minute*60000);
      if(scenario==='power_change'&&minutes>20)points.add(+start+20*60000);
      if(scheme.amount&&selected.mode==='amount')for(let day=0;day<=Math.ceil(minutes/1440);day++)for(const p of scheme.amount.periods){const at=+new Date('2026-09-30T00:00:00+08:00')+day*86400000+p.end_minute*60000;if(at>+start&&at<+end)points.add(at);}
      const sorted=Array.from(points).sort((a,b)=>a-b);
      const segments=sorted.slice(1).map((at,i)=>({started_at:new Date(sorted[i]).toISOString(),ended_at:new Date(at).toISOString(),energy_wh:Math.round((at-sorted[i])/60000*20),peak_w:scenario==='power_change'&&sorted[i]>=+start+20*60000?600:180,power_w:scenario==='power_change'&&sorted[i]>=+start+20*60000?600:180}));
      const meter = { started_at: start.toISOString(), ended_at: end.toISOString(), charged_seconds:Math.floor((+end-+start)/1000), charged_wh:segments.reduce((a,v)=>a+v.energy_wh,0),segments:scenario==='unknown'?[]:segments,review_required:scenario==='unknown' };
      if(scenario==='unused'){meter.charged_seconds=0;meter.charged_wh=0;meter.ended_at=meter.started_at;meter.segments=[];}
      setProcess(segments);
      const data = await apiPost<any>('/api/v1/admin/settings/charging-schemes/preview', { scheme, package_id: selected.id, meter, scenario:isCard||scenario.startsWith('start_')?scenario:'',wallet_cents:walletCents });
      setResult(data);if(data.cutoff_at)setProcess(segments.filter(s=>s.ended_at<=data.cutoff_at));
    } catch (e: any) { setError(e.message || '无法预览'); } finally { setPreviewing(false); }
  };
  const packageEditor = (mode: PackageMode) => {
    const packages = scheme.packages.filter(p => p.mode === mode);
    const missingPackages = invalid(`${mode}.packages`) || (invalid('packages') && packages.length === 0);
    return <Card key={mode} size="small" style={{ minWidth: 0, borderColor: missingPackages ? token.colorError : undefined }} title={<Typography.Text type={missingPackages ? 'danger' : undefined}>{modes[mode]}套餐</Typography.Text>} extra={
        <Button danger={missingPackages} disabled={scheme.packages.length >= 99} onClick={() => {
          const id = Array.from({ length: 99 }, (_, i) => i + 1).find(n => !scheme.packages.some(p => p.id === n));
          if (!id) return;
          patch({ packages: [...scheme.packages, { id, mode, name: '', price_cents: NaN, ...(mode === 'duration' ? { minutes: 0 } : mode === 'energy' ? { kwh: 1 } : {}) }] });
        }}>添加{modes[mode]}套餐</Button>
      }>
        <Typography.Paragraph type="secondary">{mode === 'amount'
          ? '支付金额作为充电预算，按已配置的金额费率结算。'
          : mode === 'duration' ? '设置套餐售价和购买时长；在线刷卡可指定其中一个时长套餐。'
          : '购买整数度电，支付价格按电费与服务费单价自动计算；实际用量按计量精度结算。'}</Typography.Paragraph>
        {packages.length === 0 ? <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={<Typography.Text type={missingPackages ? 'danger' : 'secondary'}>{`暂无${modes[mode]}套餐，点击右上角添加`}</Typography.Text>} /> :
          <Table rowKey="id" pagination={false} tableLayout="fixed" dataSource={packages}
          scroll={{ x: mode === 'amount' ? 600 : 800 }} columns={[
            { title: '套餐名称', render: (_, p) => <Input {...fieldProps(`packages.${p.id}.name`)} aria-label={`${modes[mode]}套餐名称`} value={p.name} onChange={e => patch({ packages: scheme.packages.map(a => a.id === p.id ? { ...a, name: e.target.value } : a) })} /> },
            ...(mode === 'amount' ? [] : [{ title: mode === 'duration' ? '购买时长（分钟）' : '购买电量（度）', width: 220, render: (_: unknown, p: Scheme['packages'][number]) => <InputNumber {...fieldProps(`packages.${p.id}.${mode === 'duration' ? 'minutes' : 'kwh'}`)} aria-label={mode === 'duration' ? '购买时长' : '购买电量'} min={1} max={mode === 'duration' ? 4320 : 65} precision={0} addonAfter={mode === 'duration' ? '分钟' : '度'} value={mode === 'duration' ? p.minutes : p.kwh} onChange={v => patch({ packages: scheme.packages.map(a => a.id === p.id ? { ...a, [mode === 'duration' ? 'minutes' : 'kwh']: v || 0 } : a) })} /> }]),
            { title: mode === 'amount' ? '充电预算（元）' : mode === 'energy' ? '支付价格（自动计算）' : '支付价格（元）', width: 200, render: (_, p) => mode === 'energy' ? <Typography.Text type={invalid(`packages.${p.id}.price_cents`) ? 'danger' : undefined}>{money(p.price_cents)}</Typography.Text> : <InputNumber {...fieldProps(`packages.${p.id}.price_cents`)} aria-label={mode === 'amount' ? '充电预算' : '时长套餐价格'} min={0.01} precision={2} value={Number.isFinite(p.price_cents) ? p.price_cents / 100 : null} onChange={v => patch({ packages: scheme.packages.map(a => a.id === p.id ? { ...a, price_cents: v == null ? NaN : Math.round(v * 100) } : a) })} /> },
            { title: '操作', width: 100, render: (_, p) => <Button danger onClick={() => patch({ packages: scheme.packages.filter(a => a.id !== p.id), card: { ...scheme.card, package_id: scheme.card.package_id === p.id ? 0 : scheme.card.package_id } })}>删除</Button> },
          ]} />}
      </Card>;
  };
  return <Space direction="vertical" style={{ width: '100%' }} size="large">
    <Steps current={stepIndex} onChange={index => setStep(stepIDs[index])} items={stepIDs.map(id => ({ title: ['基本信息', '金额模式', '电量模式', '时长模式', '界面展示'][id], status: validationAttempt && issues.some(issue => (stepIDs.includes(issue.step) ? issue.step : 0) === id) ? 'error' : undefined }))} />
    {step === 0 && <Space direction="vertical" size="middle" style={{ width: '100%' }}><Typography.Text>方案名称</Typography.Text><Input {...fieldProps('name')} aria-label="方案名称" maxLength={64} value={scheme.name} onChange={e => patch({ name: e.target.value })} /><Typography.Text>说明</Typography.Text><Input.TextArea aria-label="方案说明" maxLength={255} value={scheme.remark} onChange={e => patch({ remark: e.target.value })} />
      <Card size="small" title={<Typography.Text type={invalid('modes') ? 'danger' : undefined}>启用充电模式</Typography.Text>} style={{ borderColor: invalid('modes') ? token.colorError : undefined }}>
        <Space wrap>
          <Checkbox aria-invalid={invalid('modes')} style={{ color: invalid('modes') ? token.colorError : undefined }} checked={!!scheme.amount} onChange={e => patch({ amount: e.target.checked ? { algorithm: 'server_max_power', periods: [{ end_minute: 1440, tiers: [{ max_watts: 200, electric_cents: 0, service_cents: 0 }] }] } : undefined, packages: e.target.checked ? scheme.packages : scheme.packages.filter(p => p.mode !== 'amount'), ...(!e.target.checked ? { policy: { ...scheme.policy, free_minutes: 0, min_electric_cents: 0, max_minutes: 600 } } : {}) })}>金额模式</Checkbox>
          <Checkbox aria-invalid={invalid('modes')} style={{ color: invalid('modes') ? token.colorError : undefined }} checked={!!scheme.energy} onChange={e => patch({ energy: e.target.checked ? { electric_cents: 0, service_cents: 0 } : undefined, packages: e.target.checked ? scheme.packages : scheme.packages.filter(p => p.mode !== 'energy') })}>电量模式</Checkbox>
          <Checkbox aria-invalid={invalid('modes')} style={{ color: invalid('modes') ? token.colorError : undefined }} checked={durationEnabled} onChange={e => { setDurationEnabled(e.target.checked); if (!e.target.checked) patch({ packages: scheme.packages.filter(p => p.mode !== 'duration'), card: { ...scheme.card, package_id: 0, max_minutes: 600 } }); }}>时长模式</Checkbox>
        </Space>
        <Typography.Paragraph type="secondary" style={{ marginTop: 8, marginBottom: 0 }}>勾选后显示对应的配置步骤。每个启用的模式至少需要一个套餐；取消勾选会移除当前草稿中该模式的费率和套餐。</Typography.Paragraph>
      </Card>
      <Card size="small" title="充电停止规则"><Space>满充停止<Switch aria-label="满充停止" checked={scheme.stop.stop_when_full} onChange={v => patch({ stop: { stop_when_full: v } })} /></Space></Card>
    </Space>}
    {step === 1 && <>
      {scheme.amount && <>
        <Space direction="vertical" size="small">
          <Typography.Text strong>金额计费方式</Typography.Text>
          <ConfigProvider theme={{ components: { Segmented: { itemSelectedBg: token.colorPrimary, itemSelectedColor: token.colorTextLightSolid } } }}>
            <Segmented<Algorithm> aria-label="金额算法" size="large" style={{ background: token.colorPrimaryBg, border: `1px solid ${token.colorPrimaryBorder}`, padding: 4, fontWeight: 500 }} disabled={saving} value={scheme.amount.algorithm} options={[{ value: 'server_max_power', label: '最大功率' }, { value: 'server_realtime_power', label: '实时功率' }, { value: 'server_energy', label: '实际电量' }]} onChange={selectAlgorithm} />
          </ConfigProvider>
          <Typography.Text type="secondary">{scheme.amount.algorithm === 'server_max_power' ? '按各时段的峰值功率档位计费，费率单位为元/小时。' : scheme.amount.algorithm === 'server_realtime_power' ? '按实际功率片段对应的档位计费，费率单位为元/小时。' : '按各时段的实际用电量计费，费率单位为元/度。'}</Typography.Text>
        </Space>
        {scheme.amount.periods.map((p, i) => <Card key={`${scheme.amount!.algorithm}-${i}`} size="small" styles={{ header: { paddingBlock: 12 }, title: { whiteSpace: 'normal', overflow: 'visible' } }} title={
          <Space align="start" wrap size={12}>
            <Typography.Text style={{ lineHeight: '32px', fontWeight: 400 }}>开始时间</Typography.Text>
            <Typography.Text strong style={{ lineHeight: '32px' }}>{clock(i ? scheme.amount!.periods[i - 1].end_minute : 0)} ～</Typography.Text>
            <div style={{ width: 180, fontWeight: 400 }}>
                <PeriodTimeInput
                  value={p.end_minute} periods={scheme.amount!.periods} index={i}
                  initialDraft={timeDrafts.current[scheme.amount!.algorithm]?.[i]}
                  onDraftChange={text => { const drafts = timeDrafts.current[scheme.amount!.algorithm] ??= {}; drafts[i] = text; }}
                  status={invalid(`amount.periods.${i}.end_minute`) ? 'error' : undefined}
                  fixed={i === scheme.amount!.periods.length - 1} disabled={saving}
                  onChange={end_minute => updatePeriod(i, { ...p, end_minute })}
                  onValidityChange={message => setTimeErrors(current => { if (current[i] === message) return current; const next = { ...current }; if (message) next[i] = message; else delete next[i]; return next; })}
                />
            </div>
            {i === scheme.amount!.periods.length - 1 && <Typography.Text type="secondary" style={{ lineHeight: '32px', fontSize: 12, fontWeight: 400 }}>当天结束，固定为 24:00</Typography.Text>}
          </Space>
        } extra={<Space>
          <Button onClick={() => { const begin = i ? scheme.amount!.periods[i - 1].end_minute : 0; const end = Math.floor((begin + p.end_minute) / 2); if (end <= begin) return; const periods = [...scheme.amount!.periods]; periods.splice(i, 1, { ...structuredClone(p), end_minute: end }, structuredClone(p)); patch({ amount: { ...scheme.amount!, periods } }); }}>拆分时段</Button>
          {scheme.amount!.periods.length > 1 && <Popconfirm title="删除并合并到相邻时段？" onConfirm={() => { const periods = [...scheme.amount!.periods]; if (i === periods.length - 1) periods[i - 1] = { ...periods[i - 1], end_minute: 1440 }; periods.splice(i, 1); patch({ amount: { ...scheme.amount!, periods } }); }}><Button danger>删除</Button></Popconfirm>}
        </Space>}>
          {scheme.amount!.algorithm === 'server_energy' ? <Space wrap>电费 {rateInput(`amount.periods.${i}.electric_cents`, p.electric_cents, v => updatePeriod(i, { ...p, electric_cents: v }))} 服务费 {rateInput(`amount.periods.${i}.service_cents`, p.service_cents, v => updatePeriod(i, { ...p, service_cents: v }))}</Space> : <>
            <Table pagination={false} rowKey={(_, index) => String(index)} dataSource={p.tiers} columns={[
              { title: '功率范围（W，含上下限）', width: 250, render: (_, t, j) => <Space size={8}>
                <Typography.Text style={{ display: 'inline-block', minWidth: 40, textAlign: 'right' }}>{j === 0 ? 0 : p.tiers![j - 1].max_watts + 1}</Typography.Text>
                <Typography.Text type="secondary">～</Typography.Text>
                <InputNumber {...fieldProps(`amount.periods.${i}.tiers.${j}.max_watts`)} aria-label={`时段${i + 1}档位${j + 1}功率上限`} style={{ width: 90 }} min={0} max={9990} value={t.max_watts} onChange={v => updatePeriod(i, { ...p, tiers: p.tiers!.map((a, b) => b === j ? { ...a, max_watts: v || 0 } : a) })} />
              </Space> },
              ...(['electric_cents', 'service_cents'] as const).map(key => ({ title: key === 'electric_cents' ? '电费 · 元/小时' : '服务费 · 元/小时', render: (_: unknown, t: any, j: number) => rateInput(`amount.periods.${i}.tiers.${j}.${key}`, t[key], v => updatePeriod(i, { ...p, tiers: p.tiers!.map((a, b) => b === j ? { ...a, [key]: v } : a) })) })),
              { title: '操作', width: 200, render: (_, _t, j) => <Space>
                {j === p.tiers!.length - 1 && <Button disabled={p.tiers!.length >= 8 || p.tiers![p.tiers!.length - 1].max_watts >= 9990} onClick={() => updatePeriod(i, { ...p, tiers: [...p.tiers!, { max_watts: Math.min(9990, p.tiers![p.tiers!.length - 1].max_watts + 200), electric_cents: 0, service_cents: 0 }] })}>添加档位</Button>}
                <Button danger disabled={p.tiers!.length === 1} onClick={() => updatePeriod(i, { ...p, tiers: p.tiers!.filter((_, b) => b !== j) })}>删除档位</Button>
              </Space> },
            ]} />
          </>}
        </Card>)}
        <Card size="small" title="金额充电策略">
          <Form layout="horizontal" labelCol={{ flex: '160px' }} wrapperCol={{ flex: '1 1 0', style: { minWidth: 0 } }} labelWrap colon={false} requiredMark={false} style={{ maxWidth: 680 }}>
            <Form.Item label="免费时长" extra="设为 0 表示不启用。按完整充电分钟数判断，未超过该值时电费和服务费均免收；超过后按整段用量计费，不扣减免费时长。">
              <InputNumber {...fieldProps('policy.free_minutes')} aria-label="免费时长" style={{ width: '100%' }} min={0} max={scheme.policy.max_minutes} precision={0} value={scheme.policy.free_minutes} addonAfter="分钟" onChange={v => patch({ policy: { ...scheme.policy, free_minutes: v || 0 } })} />
            </Form.Item>
            <Form.Item label="最低电费" extra="设为 0.00 表示不设下限。实际充电且未满足免费条件时，电费低于该值则按该值计算，服务费另计；金额套餐预算不能低于该值。">
              <InputNumber {...fieldProps('policy.min_electric_cents')} aria-label="最低电费" style={{ width: '100%' }} min={0} precision={2} value={scheme.policy.min_electric_cents / 100} addonAfter="元" onChange={v => patch({ policy: { ...scheme.policy, min_electric_cents: Math.round((v || 0) * 100) } })} />
            </Form.Item>
            <Form.Item label="金额模式最长时长" extra="取值 1～72 小时，默认 10 小时。达到上限后停止充电并结算；预算耗尽或满足满充停止条件时可提前结束。仅用于金额模式，不限制时长和电量套餐。" style={{ marginBottom: 0 }}>
              <InputNumber {...fieldProps('policy.max_minutes')} aria-label="金额模式最长时长" style={{ width: '100%' }} min={1} max={72} value={scheme.policy.max_minutes / 60} addonAfter="小时" onChange={v => patch({ policy: { ...scheme.policy, max_minutes: Math.round((v || 0) * 60) } })} />
            </Form.Item>
          </Form>
        </Card>
        {packageEditor('amount')}
      </>}
    </>}
    {step === 2 && <>
      {scheme.energy ? <>
        <Card size="small" title="设备电量 · 全天固定单价"><Space wrap>电费<InputNumber {...fieldProps('energy.electric_cents')} aria-label="电量电费单价" min={0} precision={2} value={Number.isFinite(scheme.energy.electric_cents) ? scheme.energy.electric_cents / 100 : null} addonAfter="元/度" onChange={v => patch({ energy: { ...scheme.energy!, electric_cents: v == null ? NaN : Math.round(v * 100) } })} />服务费<InputNumber {...fieldProps('energy.service_cents')} aria-label="电量服务费单价" min={0} precision={2} value={scheme.energy.service_cents / 100} addonAfter="元/度" onChange={v => patch({ energy: { ...scheme.energy!, service_cents: Math.round((v || 0) * 100) } })} /></Space></Card>
        {packageEditor('energy')}
      </> : <Alert type="info" message="电量模式未启用，可跳过此步。" />}
    </>}
    {step === 3 && <>
      <Alert type="info" message="套餐售价和购买时长在本步配置，在线刷卡使用指定的时长套餐。" />
      {packageEditor('duration')}
      <Card size="small" title="在线刷卡"><Space direction="vertical" size="middle">
      <Space>刷卡指定套餐<Select {...fieldProps('card.package_id')} aria-label="刷卡指定套餐" style={{ width: 280 }} value={scheme.card.package_id} options={[{ value: 0, label: '关闭在线刷卡' }, ...scheme.packages.filter(p => p.mode === 'duration').map(p => ({ value: p.id, label: `${p.name || '未命名套餐'} · ${money(p.price_cents)} / ${p.minutes}分钟` }))]} onChange={v => patch({ card: { ...scheme.card, package_id: v } })} /></Space>
      <Space>刷卡累计上限<InputNumber {...fieldProps('card.max_minutes')} aria-label="刷卡累计上限" min={1} max={72} value={scheme.card.max_minutes / 60} addonAfter="小时" onChange={v => patch({ card: { ...scheme.card, max_minutes: Math.round((v || 0) * 60) } })} /></Space>
        <Typography.Text type="secondary">每次刷卡购买一份指定套餐，由服务器增加可充时长，累计时长不超过上限。</Typography.Text>
      </Space></Card>
    </>}
    {step === 4 && <Space direction="vertical">{Object.entries(displayLabels).map(([key, label]) => <Space key={key}><Switch checked={scheme.display[key]} onChange={v => patch({ display: { ...scheme.display, [key]: v } })} />{label}</Space>)}</Space>}
    <div ref={feedbackRef}>{feedback && <Alert role="alert" type="error" showIcon message={feedback} style={{ marginBottom: 12 }} />}<Space><Button disabled={stepIndex === 0 || saving} onClick={() => setStep(stepIDs[stepIndex - 1])}>上一步</Button><Button disabled={stepIndex === stepIDs.length - 1 || saving} onClick={() => setStep(stepIDs[stepIndex + 1])}>下一步</Button><Button type="primary" loading={saving} onClick={() => void save()}>保存完整方案</Button><Button onClick={() => { setPreview(true); setResult(undefined); }}>预览</Button></Space></div>
    <Drawer title="完整方案计算预览" open={preview} width={760} onClose={() => setPreview(false)}><Space direction="vertical" style={{ width: '100%' }}>
      <Typography.Text>使用当前全部输入；假设用量每分钟20Wh，功率180W／变化后600W。预览不创建订单或下发指令。</Typography.Text>
      <Select status={error === '请选择预览套餐' ? 'error' : undefined} aria-label="预览套餐" style={{ width: '100%' }} value={packageID} placeholder="选择套餐" options={scheme.packages.map(p => ({ value: p.id, label: `${modes[p.mode]} · ${p.name} · ${money(p.price_cents)}` }))} onChange={v => { setPackageID(v); setError(''); }} />
      <Select aria-label="预览场景" style={{ width: '100%' }} value={scenario} onChange={v => { setScenario(v); setResult(undefined); }} options={Object.entries({ ordinary: '普通充电60分钟', card_extend:'服务器加时成功，使用150分钟',card_failed:'服务器拒绝加时，事务回滚',card_unknown:'加时请求结果不明，按同一事件查询',start_failed:'明确未启动，全额退款',start_unknown:'启动结果不明', power_change: '20分钟低功率＋40分钟高功率', cross_period: '09:30开始跨时段', budget: '余额耗尽检查（每10分钟实际读数）', duration_limit: '达到金额最长时长', early: '提前结束61分30秒', free: '免费时长内结束',free_over:'超过免费时长1分钟', minimum: '最低电费检查', unused: '未实际充电', unknown: '缺少可靠计量／待核对' }).map(([value, label]) => ({ value, label }))} />
      {scenario.startsWith('card_')&&<Space>假设钱包余额<InputNumber min={0} precision={2} value={walletCents/100} addonAfter="元" onChange={v=>setWalletCents(Math.round((v||0)*100))}/></Space>}
      {feedback && <Alert type="error" showIcon message={feedback} />}<Button loading={previewing} onClick={() => void runPreview()}>计算</Button>
      {result && <>{result.operations?.map((o:string,i:number)=><Typography.Text key={i}>{i+1}. {o}</Typography.Text>)}{result.wallet_after!=null&&<Typography.Text>结算后钱包 {money(result.wallet_after)}</Typography.Text>}<Alert type={result.status === 'calculated' ? 'success' : 'warning'} message={result.reason} />{result.cutoff_at&&<Typography.Text>按输入的计量读数触发预算截止：{new Date(result.cutoff_at).toLocaleTimeString()}</Typography.Text>}<Typography.Text>支付 {money(result.paid_cents)} · 套餐 {result.package.name} · 假设过程 {process.length} 段</Typography.Text><Table rowKey="started_at" pagination={false} dataSource={process} columns={[{ title: '起止', render: (_, p: any) => `${new Date(p.started_at).toLocaleTimeString()}～${new Date(p.ended_at).toLocaleTimeString()}` }, { title: '功率W', dataIndex: 'power_w' }, { title: '实际Wh', dataIndex: 'energy_wh' }]} />{result.status === 'calculated' && <>{result.raw_fee&&<><Table size="small" rowKey="started_at" pagination={false} dataSource={result.raw_fee.fragments||[]} columns={[{title:'计费片段',render:(_,f:any)=>new Date(f.started_at).toLocaleTimeString()+'～'+new Date(f.ended_at).toLocaleTimeString()},{title:'档位 / 峰值W',dataIndex:'power_w'},{title:'计费用量',render:(_,f:any)=>f.quantity+f.unit},{title:'电费 / 服务费单价',render:(_,f:any)=>money(f.electric_rate)+' / '+money(f.service_rate)},{title:'原始电费 / 服务费（未取整分）',render:(_,f:any)=>f.electric_cents+' / '+f.service_cents}]} /><Typography.Text>基础合计 {money(result.raw_fee.total_cents)}；策略后 {money(result.policy_fee?.total_cents??0)}；预算截断 {money(Math.max(0,(result.policy_fee?.total_cents??0)-result.settlement.total_cents))}</Typography.Text></>}<Typography.Text>电费 {money(result.settlement.electric_cents)} · 服务费 {money(result.settlement.service_cents)}</Typography.Text><Typography.Text>策略：免费时长 {scheme.policy.free_minutes} 分钟；最低电费 {money(scheme.policy.min_electric_cents)}（仅金额模式）</Typography.Text><Typography.Text strong>实收 {money(result.settlement.total_cents)} · 退款 {money(result.refund_cents)}</Typography.Text></>}</>}
    </Space></Drawer>
  </Space>;
}
