import { useState } from 'react';
import { Alert, Button, Card, Checkbox, Drawer, Input, InputNumber, Popconfirm, Select, Space, Steps, Switch, Table, Typography } from 'antd';
import { apiPost } from '../../api/client';
import { Algorithm, PackageMode, Scheme, clock, displayLabels, missingInputs, modes, money, priceEnergy } from './model';

export default function SchemeEditor({ value: initial, onSave, saving = false }: { value: Scheme; onSave: (s: Scheme) => Promise<void>; saving?: boolean }) {
  const [scheme, setScheme] = useState(initial);
  const [step, setStep] = useState(0);
  const [error, setError] = useState('');
  const [preview, setPreview] = useState(false);
  const [scenario, setScenario] = useState('ordinary');
  const [packageID, setPackageID] = useState<number>();
  const [result, setResult] = useState<any>();
  const [walletCents,setWalletCents]=useState(1000);
  const [previewing, setPreviewing] = useState(false);
  const [process, setProcess] = useState<any[]>([]);
  const change = (s: Scheme) => { setScheme(priceEnergy(s)); setError(''); setResult(undefined); };
  const patch = (partial: Partial<Scheme>) => change({ ...scheme, ...partial });
  const updatePeriod = (i: number, p: any) => patch({ amount: { ...scheme.amount!, periods: scheme.amount!.periods.map((v, j) => j === i ? p : v) } });
  const newPeriod = () => scheme.amount?.algorithm === 'server_energy'
    ? { end_minute: 1440, electric_cents: NaN, service_cents: 0 }
    : { end_minute: 1440, tiers: [{ max_watts: 200, electric_cents: NaN, service_cents: 0 }] };
  const rateInput = (value: number | undefined, onChange: (v: number) => void) => <InputNumber aria-label="费率（元）" min={0} precision={2} value={Number.isFinite(value) ? value! / 100 : null} onChange={v => onChange(v == null ? NaN : Math.round(v * 100))} addonAfter={scheme.amount?.algorithm === 'server_energy' ? '元/度' : '元/小时'} />;
  const save = async () => { const missing = missingInputs(scheme); if (missing.length) { setError(`请补充：${missing.join('、')}`); return; } try { await onSave(scheme); } catch (e: any) { setError(e.message || '保存失败'); } };
  const runPreview = async () => {
    const missing = missingInputs(scheme);
    if (missing.length) { setError(`请补充：${missing.join('、')}`); return; }
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
  return <Space direction="vertical" style={{ width: '100%' }} size="large">
    <Steps current={step} onChange={setStep} items={['基本信息', '模式与费率', '支付套餐', '配置', '用户界面展示'].map(title => ({ title }))} />
    {error && <Alert type="error" showIcon message={error} />}
    {step === 0 && <Space direction="vertical" style={{ width: '100%' }}><Typography.Text>方案名称</Typography.Text><Input aria-label="方案名称" maxLength={64} value={scheme.name} onChange={e => patch({ name: e.target.value })} /><Typography.Text>说明</Typography.Text><Input.TextArea aria-label="方案说明" maxLength={255} value={scheme.remark} onChange={e => patch({ remark: e.target.value })} /></Space>}
    {step === 1 && <>
      <Space wrap><Checkbox checked={!!scheme.amount} onChange={e => patch({ amount: e.target.checked ? { algorithm: 'server_max_power', periods: [{ end_minute: 1440, tiers: [{ max_watts: 200, electric_cents: NaN, service_cents: 0 }] }] } : undefined, packages: e.target.checked ? scheme.packages : scheme.packages.filter(p => p.mode !== 'amount') })}>启用金额入口</Checkbox>
        <Checkbox checked={!!scheme.energy} onChange={e => patch({ energy: e.target.checked ? { electric_cents: NaN, service_cents: 0 } : undefined, packages: e.target.checked ? scheme.packages : scheme.packages.filter(p => p.mode !== 'energy') })}>启用电量入口</Checkbox><Typography.Text>时长入口通过添加时长套餐启用</Typography.Text></Space>
      {scheme.amount && <>
        <Select aria-label="金额算法" style={{ width: 280 }} value={scheme.amount.algorithm} options={[{ value: 'server_max_power', label: '最大功率 · 各时段峰值 · 元/小时' }, { value: 'server_realtime_power', label: '实时功率 · 各片段 · 元/小时' }, { value: 'server_energy', label: '实际电量 · 各时段 · 元/度' }]} onChange={(algorithm: Algorithm) => patch({ amount: { algorithm, periods: algorithm === scheme.amount!.algorithm ? scheme.amount!.periods : algorithm === 'server_energy' ? [{ end_minute: 1440, electric_cents: NaN, service_cents: 0 }] : [{ end_minute: 1440, tiers: [{ max_watts: 200, electric_cents: NaN, service_cents: 0 }] }] } })} />
        {scheme.amount.periods.map((p, i) => <Card key={i} size="small" title={`${clock(i ? scheme.amount!.periods[i - 1].end_minute : 0)}～${clock(p.end_minute)}`} extra={<Space>
          <Popconfirm title="在此时段中间拆分？" onConfirm={() => { const begin = i ? scheme.amount!.periods[i - 1].end_minute : 0; const end = Math.floor((begin + p.end_minute) / 2); if (end <= begin) return; const periods = [...scheme.amount!.periods]; periods.splice(i, 1, { ...structuredClone(p), end_minute: end }, structuredClone(p)); patch({ amount: { ...scheme.amount!, periods } }); }}><Button>拆分时段</Button></Popconfirm>
          {scheme.amount!.periods.length > 1 && <Popconfirm title="删除并合并到相邻时段？" onConfirm={() => { const periods = [...scheme.amount!.periods]; if (i === periods.length - 1) periods[i - 1] = { ...periods[i - 1], end_minute: 1440 }; periods.splice(i, 1); patch({ amount: { ...scheme.amount!, periods } }); }}><Button danger>删除</Button></Popconfirm>}
        </Space>}>
          <Space>结束时间（当天分钟）<InputNumber aria-label="时段结束分钟" min={1} max={1440} value={p.end_minute} disabled={i === scheme.amount!.periods.length - 1} onChange={v => updatePeriod(i, { ...p, end_minute: v || 0 })} /></Space>
          {scheme.amount!.algorithm === 'server_energy' ? <Space wrap>电费 {rateInput(p.electric_cents, v => updatePeriod(i, { ...p, electric_cents: v }))} 服务费 {rateInput(p.service_cents, v => updatePeriod(i, { ...p, service_cents: v }))}</Space> : <>
            <Table pagination={false} rowKey={(_, index) => String(index)} dataSource={p.tiers} columns={[
              { title: '功率上限（W，含边界）', render: (_, t, j) => <InputNumber min={0} max={9990} value={t.max_watts} onChange={v => updatePeriod(i, { ...p, tiers: p.tiers!.map((a, b) => b === j ? { ...a, max_watts: v || 0 } : a) })} /> },
              ...(['electric_cents', 'service_cents'] as const).map(key => ({ title: key === 'electric_cents' ? '电费 · 元/小时' : '服务费 · 元/小时', render: (_: unknown, t: any, j: number) => rateInput(t[key], v => updatePeriod(i, { ...p, tiers: p.tiers!.map((a, b) => b === j ? { ...a, [key]: v } : a) })) })),
              { title: '操作', render: (_, _t, j) => <Button danger disabled={p.tiers!.length === 1} onClick={() => updatePeriod(i, { ...p, tiers: p.tiers!.filter((_, b) => b !== j) })}>删除档位</Button> },
            ]} />
            <Button disabled={p.tiers!.length >= 8 || p.tiers![p.tiers!.length - 1].max_watts >= 9990} onClick={() => updatePeriod(i, { ...p, tiers: [...p.tiers!, { max_watts: Math.min(9990, p.tiers![p.tiers!.length - 1].max_watts + 200), electric_cents: NaN, service_cents: 0 }] })}>添加末尾档位</Button>
          </>}
        </Card>)}
      </>}
      {scheme.energy && <Card size="small" title="设备电量 · 全天固定单价"><Space wrap>电费<InputNumber min={0} precision={2} value={Number.isFinite(scheme.energy.electric_cents) ? scheme.energy.electric_cents / 100 : null} addonAfter="元/度" onChange={v => patch({ energy: { ...scheme.energy!, electric_cents: v == null ? NaN : Math.round(v * 100) } })} />服务费<InputNumber min={0} precision={2} value={scheme.energy.service_cents / 100} addonAfter="元/度" onChange={v => patch({ energy: { ...scheme.energy!, service_cents: Math.round((v || 0) * 100) } })} /></Space></Card>}
    </>}
    {step === 2 && <>
      <Table rowKey="id" pagination={false} dataSource={scheme.packages} columns={[
        { title: '模式', render: (_, p) => modes[p.mode] },
        { title: '套餐名称', render: (_, p) => <Input value={p.name} onChange={e => patch({ packages: scheme.packages.map(a => a.id === p.id ? { ...a, name: e.target.value } : a) })} /> },
        { title: '支付价格（元）', render: (_, p) => p.mode === 'energy' ? money(p.price_cents) : <InputNumber min={0.01} precision={2} value={Number.isFinite(p.price_cents) ? p.price_cents / 100 : null} onChange={v => patch({ packages: scheme.packages.map(a => a.id === p.id ? { ...a, price_cents: v == null ? NaN : Math.round(v * 100) } : a) })} /> },
        { title: '权益', render: (_, p) => p.mode === 'amount' ? '金额作为充电预算' : <InputNumber min={1} max={p.mode === 'duration' ? 4320 : 65} precision={0} addonAfter={p.mode === 'duration' ? '分钟' : '度'} value={p.mode === 'duration' ? p.minutes : p.kwh} onChange={v => patch({ packages: scheme.packages.map(a => a.id === p.id ? { ...a, [p.mode === 'duration' ? 'minutes' : 'kwh']: v || 0 } : a) })} /> },
        { title: '操作', render: (_, p) => <Button danger onClick={() => patch({ packages: scheme.packages.filter(a => a.id !== p.id), card: { ...scheme.card, package_id: scheme.card.package_id === p.id ? 0 : scheme.card.package_id } })}>删除</Button> },
      ]} />
      <Space>{(['amount', 'duration', 'energy'] as PackageMode[]).filter(m => m === 'duration' || m === 'amount' && scheme.amount || m === 'energy' && scheme.energy).map(mode => <Button key={mode} onClick={() => { const id = Array.from({ length: 99 }, (_, i) => i + 1).find(n => !scheme.packages.some(p => p.id === n)); if (!id) return; patch({ packages: [...scheme.packages, { id, mode, name: '', price_cents: NaN, ...(mode === 'duration' ? { minutes: 0 } : mode === 'energy' ? { kwh: 1 } : {}) }] }); }}>添加{modes[mode]}套餐</Button>)}</Space>
      <Alert type="info" message="电量套餐只购买整数度，价格自动计算；实际用量按计量精度结算。" />
    </>}
    {step === 3 && <Space direction="vertical" size="middle">
      {scheme.amount && <><Space>免费时长<InputNumber min={0} max={scheme.policy.max_minutes} value={scheme.policy.free_minutes} addonAfter="分钟" onChange={v => patch({ policy: { ...scheme.policy, free_minutes: v || 0 } })} /></Space><Space>最低电费<InputNumber min={0} precision={2} value={scheme.policy.min_electric_cents / 100} addonAfter="元" onChange={v => patch({ policy: { ...scheme.policy, min_electric_cents: Math.round((v || 0) * 100) } })} /></Space><Space>金额模式最长时长<InputNumber min={1} max={72} value={scheme.policy.max_minutes / 60} addonAfter="小时" onChange={v => patch({ policy: { ...scheme.policy, max_minutes: Math.round((v || 0) * 60) } })} /></Space></>}
      <Space>满充停止<Switch checked={scheme.stop.stop_when_full} onChange={v => patch({ stop: { stop_when_full: v } })} /></Space>
      <Space>刷卡指定套餐<Select style={{ width: 280 }} value={scheme.card.package_id} options={[{ value: 0, label: '关闭在线刷卡' }, ...scheme.packages.filter(p => p.mode === 'duration').map(p => ({ value: p.id, label: `${p.name || '未命名套餐'} · ${money(p.price_cents)} / ${p.minutes}分钟` }))]} onChange={v => patch({ card: { ...scheme.card, package_id: v } })} /></Space>
      <Space>刷卡累计上限<InputNumber min={1} max={72} value={scheme.card.max_minutes / 60} addonAfter="小时" onChange={v => patch({ card: { ...scheme.card, max_minutes: Math.round((v || 0) * 60) } })} /></Space>
      <Alert type="info" message="免费、最低电费只用于金额模式。损耗与渠道系数组合规则待确认，当前为关闭损耗、1倍系数。" />
    </Space>}
    {step === 4 && <Space direction="vertical">{Object.entries(displayLabels).map(([key, label]) => <Space key={key}><Switch checked={scheme.display[key]} onChange={v => patch({ display: { ...scheme.display, [key]: v } })} />{label}</Space>)}</Space>}
    <Space><Button disabled={step === 0 || saving} onClick={() => setStep(step - 1)}>上一步</Button><Button disabled={step === 4 || saving} onClick={() => setStep(step + 1)}>下一步</Button><Button type="primary" loading={saving} onClick={() => void save()}>保存完整方案</Button><Button onClick={() => { setPreview(true); setResult(undefined); }}>预览</Button></Space>
    <Drawer title="完整方案计算预览" open={preview} width={760} onClose={() => setPreview(false)}><Space direction="vertical" style={{ width: '100%' }}>
      <Typography.Text>使用当前全部输入；假设用量每分钟20Wh，功率180W／变化后600W。预览不创建订单或下发指令。</Typography.Text>
      <Select aria-label="预览套餐" style={{ width: '100%' }} value={packageID} placeholder="选择套餐" options={scheme.packages.map(p => ({ value: p.id, label: `${modes[p.mode]} · ${p.name} · ${money(p.price_cents)}` }))} onChange={setPackageID} />
      <Select aria-label="预览场景" style={{ width: '100%' }} value={scenario} onChange={v => { setScenario(v); setResult(undefined); }} options={Object.entries({ ordinary: '普通充电60分钟', card_extend:'服务器加时成功，使用150分钟',card_failed:'服务器拒绝加时，事务回滚',card_unknown:'加时请求结果不明，按同一事件查询',start_failed:'明确未启动，全额退款',start_unknown:'启动结果不明', power_change: '20分钟低功率＋40分钟高功率', cross_period: '09:30开始跨时段', budget: '余额耗尽检查（每10分钟实际读数）', duration_limit: '达到金额最长时长', early: '提前结束61分30秒', free: '免费时长内结束',free_over:'超过免费时长1分钟', minimum: '最低电费检查', unused: '未实际充电', unknown: '缺少可靠计量／待核对' }).map(([value, label]) => ({ value, label }))} />
      {scenario.startsWith('card_')&&<Space>假设钱包余额<InputNumber min={0} precision={2} value={walletCents/100} addonAfter="元" onChange={v=>setWalletCents(Math.round((v||0)*100))}/></Space>}
      <Button loading={previewing} onClick={() => void runPreview()}>计算</Button>{error && <Alert type="error" message={error} />}
      {result && <>{result.operations?.map((o:string,i:number)=><Typography.Text key={i}>{i+1}. {o}</Typography.Text>)}{result.wallet_after!=null&&<Typography.Text>结算后钱包 {money(result.wallet_after)}</Typography.Text>}<Alert type={result.status === 'calculated' ? 'success' : 'warning'} message={result.reason} />{result.cutoff_at&&<Typography.Text>按输入的计量读数触发预算截止：{new Date(result.cutoff_at).toLocaleTimeString()}</Typography.Text>}<Typography.Text>支付 {money(result.paid_cents)} · 套餐 {result.package.name} · 假设过程 {process.length} 段</Typography.Text><Table rowKey="started_at" pagination={false} dataSource={process} columns={[{ title: '起止', render: (_, p: any) => `${new Date(p.started_at).toLocaleTimeString()}～${new Date(p.ended_at).toLocaleTimeString()}` }, { title: '功率W', dataIndex: 'power_w' }, { title: '实际Wh', dataIndex: 'energy_wh' }]} />{result.status === 'calculated' && <>{result.raw_fee&&<><Table size="small" rowKey="started_at" pagination={false} dataSource={result.raw_fee.fragments||[]} columns={[{title:'计费片段',render:(_,f:any)=>new Date(f.started_at).toLocaleTimeString()+'～'+new Date(f.ended_at).toLocaleTimeString()},{title:'档位 / 峰值W',dataIndex:'power_w'},{title:'计费用量',render:(_,f:any)=>f.quantity+f.unit},{title:'电费 / 服务费单价',render:(_,f:any)=>money(f.electric_rate)+' / '+money(f.service_rate)},{title:'原始电费 / 服务费（未取整分）',render:(_,f:any)=>f.electric_cents+' / '+f.service_cents}]} /><Typography.Text>基础合计 {money(result.raw_fee.total_cents)}；策略后 {money(result.policy_fee?.total_cents??0)}；预算截断 {money(Math.max(0,(result.policy_fee?.total_cents??0)-result.settlement.total_cents))}</Typography.Text></>}<Typography.Text>电费 {money(result.settlement.electric_cents)} · 服务费 {money(result.settlement.service_cents)}</Typography.Text><Typography.Text>策略：免费时长 {scheme.policy.free_minutes} 分钟；最低电费 {money(scheme.policy.min_electric_cents)}（仅金额模式）</Typography.Text><Typography.Text strong>实收 {money(result.settlement.total_cents)} · 退款 {money(result.refund_cents)}</Typography.Text></>}</>}
    </Space></Drawer>
  </Space>;
}
