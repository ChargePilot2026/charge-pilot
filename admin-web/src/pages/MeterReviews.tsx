import { useEffect, useState } from 'react';
import { Alert, Button, Descriptions, Drawer, Form, Input, InputNumber, Modal, Select, Space, Table, Tag, Typography, message } from 'antd';
import dayjs from 'dayjs';
import { adminSession, apiGet, apiPost } from '../api/client';
import { LoadError } from '../components/LoadError';

type Segment = { started_at:string; ended_at:string; energy_wh:number };
type Source = { meter:{ started_at:string; ended_at:string; charged_wh:number; charged_seconds:number; segments?:Segment[] }; rule:{ time_of_use:{start:string;end:string;electric_price_cents:number}[]; service_cents_per_kwh:number } };
type Review = { id:number; status:string; first_reviewer_id:number; second_reviewer_id:number|null; reason:string; reject_reason:string|null; corrected:Source };
type Entry = { charge_order_id:number; order_no:string; reason:string; status:string; source:Source; reviews:Review[] };
const labels:Record<string,string> = {pending:'待核实',resolved:'已完成计费',awaiting_second:'待第二人复核',approved:'已复核通过',rejected:'已拒绝'};
const format = (v:string)=>dayjs(v).format('YYYY-MM-DD HH:mm:ss');
export default function MeterReviews(){
 const [rows,setRows]=useState<Entry[]>([]),[total,setTotal]=useState(0),[page,setPage]=useState(1),[status,setStatus]=useState('pending'),[keyword,setKeyword]=useState('');
 const [loading,setLoading]=useState(false),[error,setError]=useState(''),[selected,setSelected]=useState<Entry|null>(null),[editing,setEditing]=useState(false),[rejecting,setRejecting]=useState(false),[saving,setSaving]=useState(false);
 const [reason,setReason]=useState(''),[request,setRequest]=useState(''),[epoch,setEpoch]=useState<string|null>(null);
 const [form]=Form.useForm();
 let actor=0,canReview=false;
 try{const p=JSON.parse(localStorage.getItem('cp_admin')||'null');actor=Number(p?.admin_user_id);canReview=p?.role==='customer_finance'&&p?.permissions?.includes('billing.meter.review');}catch{}
 async function load(){setLoading(true);setError('');try{const data=await apiGet<{items:Entry[];total:number}>('/api/v1/admin/billing/meter-reviews',{page,page_size:20,status,keyword});setRows(data.items);setTotal(data.total);}catch(e:any){setError(e.message||'核实队列读取失败');}finally{setLoading(false);}}
 useEffect(()=>{void load();},[page,status]);
 const latest=selected?.reviews[0];
 function open(row:Entry){setSelected(row);setEditing(false);setEpoch(adminSession.epoch());}
 function propose(){if(!selected)return;setRequest(crypto.randomUUID());setEditing(true);form.setFieldsValue({reason:'',segments:[{started_at:format(selected.source.meter.started_at),ended_at:format(selected.source.meter.ended_at),energy_wh:selected.source.meter.charged_wh}]});}
 async function save(){if(!selected||saving)return;try{const values=await form.validateFields();if(epoch!==adminSession.epoch())throw new Error('登录账号已变化，请重新打开记录');setSaving(true);const segments=values.segments.map((s:Segment)=>{const start=dayjs(s.started_at),end=dayjs(s.ended_at);if(!start.isValid()||!end.isValid())throw new Error('请填写有效的起止时间');return {...s,started_at:start.toISOString(),ended_at:end.toISOString()};});await apiPost(`/api/v1/admin/billing/meter-reviews/${selected.charge_order_id}/propose`,{request_id:request,reason:values.reason,segments});message.success('首次核实已保存，等待另一名财务复核');setSelected(null);setEditing(false);await load();}catch(e:any){if(!e.errorFields)message.error(e.message||'提交失败');}finally{setSaving(false);}}
 async function decide(approve:boolean){if(!selected||!latest||saving)return;try{if(epoch!==adminSession.epoch())throw new Error('登录账号已变化，请重新打开记录');setSaving(true);await apiPost(`/api/v1/admin/billing/meter-reviews/${selected.charge_order_id}/decide`,{review_id:latest.id,approve,reason});message.success(approve?'复核通过，订单已恢复计费':'已拒绝，可补充依据后重新提交');setSelected(null);setRejecting(false);await load();}catch(e:any){message.error(e.message||'审核失败');}finally{setSaving(false);}}
 return <>
  <Space wrap style={{marginBottom:12}}><Input aria-label="核实订单号" placeholder="订单号" value={keyword} onChange={e=>setKeyword(e.target.value)}/><Select aria-label="核实状态" value={status} onChange={v=>{setStatus(v);setPage(1);}} options={[{value:'pending',label:'待核实'},{value:'resolved',label:'已完成计费'},{value:'',label:'全部'}]}/><Button onClick={()=>void load()} loading={loading}>查询核实队列</Button></Space>
  {error&&<LoadError title="计量核实队列加载失败" detail={error} onRetry={() => void load()}/>}
  <Table rowKey="charge_order_id" loading={loading} dataSource={rows} pagination={{current:page,pageSize:20,total,onChange:setPage}} columns={[{title:'订单号',dataIndex:'order_no'},{title:'原因',dataIndex:'reason'},{title:'实际电量',render:(_,r)=>`${r.source?.meter?.charged_wh??'—'} Wh`},{title:'状态',render:(_,r)=><Tag>{labels[r.status==='resolved'?'resolved':r.reviews[0]?.status||r.status]}</Tag>},{title:'操作',render:(_,r)=><Button disabled={!r.source?.meter || !r.source?.rule} onClick={()=>open(r)}>查看核实</Button>}]}/>
  <Drawer title="实际计量核实" width={800} open={!!selected} onClose={()=>{if(!saving){setSelected(null);setEditing(false);}}}>
   {selected&&<>
    <Alert type="info" showIcon message="仅补充分段实际读数，不改动设备总电量和原始回执。首次核实与第二人复核通过后自动恢复计费。读数时间按当前浏览器时区显示；冻结电价时段使用北京时间。"/>
    {!selected.source.rule.time_of_use?.length&&<Alert type="error" message="冻结价格快照缺少费率，补充分段读数无法恢复计费，请先核查原始支付记录。"/>}<Descriptions column={1} items={[{key:'order',label:'订单',children:selected.order_no},{key:'time',label:'原始时段',children:`${format(selected.source.meter.started_at)} 至 ${format(selected.source.meter.ended_at)}`},{key:'wh',label:'实际总电量',children:`${selected.source.meter.charged_wh} Wh`},{key:'rates',label:'冻结电价（分/kWh）',children:(selected.source.rule.time_of_use||[]).map(p=>`${p.start}–${p.end}: ${p.electric_price_cents}`).join('；')}]}/>
    {selected.reviews.map(r=><div key={r.id} style={{marginBottom:16}}><Typography.Text strong>{labels[r.status]} · 首次核实人 {r.first_reviewer_id} · 复核人 {r.second_reviewer_id||'—'}</Typography.Text><p>依据：{r.reason}{r.reject_reason?`；拒绝：${r.reject_reason}`:''}</p><Table size="small" pagination={false} rowKey="started_at" dataSource={r.corrected.meter.segments} columns={[{title:'开始',dataIndex:'started_at',render:format},{title:'结束',dataIndex:'ended_at',render:format},{title:'电量 Wh',dataIndex:'energy_wh'}]}/></div>)}
    {canReview&&selected.status==='pending'&&!editing&&selected.source.rule.time_of_use?.length>0&&(!latest||latest.status==='rejected')&&<Button type="primary" onClick={propose}>填写分段读数</Button>}
    {canReview&&latest?.status==='awaiting_second'&&latest.first_reviewer_id!==actor&&<Space><Button type="primary" loading={saving} onClick={()=>void decide(true)}>确认读数并恢复计费</Button><Button danger onClick={()=>{setReason('');setRejecting(true);}}>拒绝读数</Button></Space>}
    {latest?.status==='awaiting_second'&&latest.first_reviewer_id===actor&&<Alert type="info" message="等待另一名财务人员复核，不能自行确认。"/>}
    {editing&&<Form form={form} layout="vertical"><Form.Item name="reason" label="核实依据" rules={[{required:true,whitespace:true,max:500}]}><Input.TextArea rows={2}/></Form.Item><Form.List name="segments">{(fields,{add,remove})=><>{fields.map(field=><Space key={field.key} align="baseline"><Form.Item name={[field.name,'started_at']} label="开始时间" rules={[{required:true}]}><Input placeholder="YYYY-MM-DD HH:mm:ss"/></Form.Item><Form.Item name={[field.name,'ended_at']} label="结束时间" rules={[{required:true}]}><Input placeholder="YYYY-MM-DD HH:mm:ss"/></Form.Item><Form.Item name={[field.name,'energy_wh']} label="实际 Wh" rules={[{required:true}]}><InputNumber min={0} max={4294967295} precision={0}/></Form.Item><Button onClick={()=>remove(field.name)}>删除该段</Button></Space>)}<Button onClick={()=>add({energy_wh:0})}>添加计量分段</Button></>}</Form.List><Space style={{marginTop:16}}><Button type="primary" loading={saving} onClick={()=>void save()}>提交首次核实</Button><Button disabled={saving} onClick={()=>setEditing(false)}>取消填写</Button></Space></Form>}
   </>}
  </Drawer>
  <Modal title="拒绝分段读数" open={rejecting} onCancel={()=>setRejecting(false)} confirmLoading={saving} okButtonProps={{disabled:!reason.trim()}} onOk={()=>void decide(false)}><Input.TextArea aria-label="拒绝依据" value={reason} maxLength={500} onChange={e=>setReason(e.target.value)} placeholder="拒绝依据"/></Modal>
 </>;
}
