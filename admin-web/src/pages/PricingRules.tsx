import { useEffect, useState } from 'react';
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Space, Table, Tag, message } from 'antd';
import { apiGet, apiPost } from '../api/client';

type Period = { period: string; start: string; end: string; electric_price_cents: number; service_price_cents?: number };
type Rule = { id: number; name: string; station_id: number; station_name?: string; mode: string; version: number; status: string; time_of_use_json: Period[]; service_fee_cents_per_kwh: number; service_fee_cents_per_min: number; min_charge_cents: number };
type Publication = { request_id: string; name: string; station_id: number; expected_version: number; mode: string; time_of_use: Period[]; service_fee_cents_per_kwh: number; service_fee_cents_per_min: number; min_charge_cents: number };

export default function PricingRules() {
 const [rules,setRules]=useState<Rule[]>([]);
 const [permissions,setPermissions]=useState<string[]>([]);
 const [loading,setLoading]=useState(false);
 const [saving,setSaving]=useState(false);
 const [open,setOpen]=useState(false);
 const [pending,setPending]=useState<Publication>();
 const [publicationError,setPublicationError]=useState('');
 const [form]=Form.useForm();
 const load=async()=>{setLoading(true);try{const r=await apiGet<{items:Rule[];permissions:string[]}>('/api/v1/admin/settings/charge-rules');setRules(r.items);setPermissions(r.permissions);}catch(e:any){message.error(e.message);}finally{setLoading(false);}};
 useEffect(()=>{void load();},[]);
 const edit=(rule?:Rule)=>{setPending(undefined);setPublicationError('');form.resetFields();form.setFieldsValue(rule?{...rule,time_of_use:rule.time_of_use_json?.length?rule.time_of_use_json:[{period:'flat',start:'00:00',end:'24:00',electric_price_cents:50}]}:{mode:'kwh',service_fee_cents_per_kwh:0,service_fee_cents_per_min:0,min_charge_cents:0,time_of_use:[{period:'flat',start:'00:00',end:'24:00',electric_price_cents:50}]});setOpen(true);};
 const publish=async()=>{
  setSaving(true);setPublicationError('');
  try {
   const values=await form.validateFields();
   const candidate={name:values.name,station_id:values.station_id,mode:values.mode,time_of_use:values.time_of_use.map((p:Period)=>({...p,service_price_cents:p.service_price_cents??undefined})),service_fee_cents_per_kwh:values.service_fee_cents_per_kwh,service_fee_cents_per_min:values.service_fee_cents_per_min,min_charge_cents:values.min_charge_cents};
   // Preserve the exact uncertain publication until the operator changes inputs.
   const body:Publication=pending && JSON.stringify(candidate)===JSON.stringify((({request_id:_,expected_version:__,...v})=>v)(pending)) ? pending : {...candidate,request_id:crypto.randomUUID(),expected_version:Math.max(0,...rules.filter(r=>r.station_id===values.station_id).map(r=>r.version))};
   setPending(body);
   const result=await apiPost<{version:number}>('/api/v1/admin/settings/charge-rules',body);
   message.success(`已发布 v${result.version}`);setOpen(false);setPending(undefined);await load();
  }catch(e:any){if(!e.errorFields)setPublicationError(e.message||'发布失败');}finally{setSaving(false);}
 };
 const disable=(rule:Rule)=>Modal.confirm({title:'停用计费规则',content:`停用“${rule.name}” v${rule.version} 后，该站点没有有效规则时将无法发起新支付。已有支付保留原规则快照。`,okText:'停用',onOk:async()=>{await apiPost(`/api/v1/admin/settings/charge-rules/${rule.id}/disable`);message.success('已停用');await load();}});
 return <>
  <Space style={{marginBottom:12}}><Button onClick={()=>void load()} loading={loading}>刷新规则</Button>{permissions.includes('pricing.rule.create')&&<Button type="primary" onClick={()=>edit()}>发布规则</Button>}</Space>
  <Alert type="info" showIcon message="发布后立即用于该站点的新支付；历史版本与已付款订单的快照保留。电价时段须完整覆盖 00:00–24:00。" style={{marginBottom:12}}/>
  <Table rowKey="id" dataSource={rules} loading={loading} scroll={{x:1050}} columns={[
   {title:'名称 / 站点',render:(_:unknown,r:Rule)=><>{r.name}<div>{r.station_name||'未绑定站点'}（{r.station_id||'—'}）</div></>},
   {title:'版本',dataIndex:'version',render:(v:number)=>`v${v}`},
   {title:'模式',dataIndex:'mode'},
   {title:'电价时段（分/kWh）',render:(_:unknown,r:Rule)=><>{Array.isArray(r.time_of_use_json)?r.time_of_use_json.map((p,i)=><div key={i}>{p.start}–{p.end}：{p.electric_price_cents}{p.service_price_cents!==undefined?` / 服务费 ${p.service_price_cents}`:''}</div>):'未配置'}</>},
   {title:'服务费',render:(_:unknown,r:Rule)=><>{r.service_fee_cents_per_kwh} 分/kWh；{r.service_fee_cents_per_min} 分/分钟</>},
   {title:'起步价（分）',dataIndex:'min_charge_cents'},
   {title:'状态',dataIndex:'status',render:(s:string)=><Tag color={s==='active'?'green':'default'}>{s==='active'?'已启用':'已停用'}</Tag>},
   {title:'操作',render:(_:unknown,r:Rule)=><Space>{permissions.includes('pricing.rule.create')&&<Button type="link" onClick={()=>edit(r)}>调整并发布</Button>}{r.status==='active'&&permissions.includes('pricing.rule.update')&&<Button type="link" danger onClick={()=>disable(r)}>停用</Button>}</Space>}
  ]}/>
  <Modal title="发布计费规则" open={open} width={900} onCancel={()=>setOpen(false)} onOk={()=>void publish()} confirmLoading={saving} okText="发布新版本" destroyOnClose>
   {publicationError&&<Alert type="error" showIcon message={publicationError} style={{marginBottom:12}}/>}
   <Form form={form} name="pricing_publication" layout="vertical">
    <Space align="start"><Form.Item name="name" label="规则名称" rules={[{required:true,whitespace:true,max:128}]}><Input maxLength={128}/></Form.Item><Form.Item name="station_id" label="站点 ID" rules={[{required:true}]}><InputNumber min={1} precision={0}/></Form.Item><Form.Item name="mode" label="计费模式" rules={[{required:true}]}><Select style={{width:160}} options={[{value:'kwh',label:'电量服务费'},{value:'minute',label:'时长服务费'},{value:'mixed',label:'电量与时长服务费'}]}/></Form.Item></Space>
    <Space align="start"><Form.Item name="service_fee_cents_per_kwh" label="服务费（分/kWh）" rules={[{required:true}]}><InputNumber min={0} max={1000000} precision={0}/></Form.Item><Form.Item name="service_fee_cents_per_min" label="服务费（分/分钟）" rules={[{required:true}]}><InputNumber min={0} max={1000000} precision={0}/></Form.Item><Form.Item name="min_charge_cents" label="起步价（分）" rules={[{required:true}]}><InputNumber min={0} max={1000000} precision={0}/></Form.Item></Space>
    <Form.List name="time_of_use">{(fields,{add,remove})=><>{fields.map(({key,name,...rest})=><Space key={key} align="start" wrap>
     <Form.Item {...rest} name={[name,'period']} label="时段名称" rules={[{required:true,max:32}]}><Input style={{width:120}}/></Form.Item>
     <Form.Item {...rest} name={[name,'start']} label="开始（HH:mm）" rules={[{required:true,pattern:/^\d{2}:\d{2}$/}]}><Input style={{width:110}} placeholder="00:00"/></Form.Item>
     <Form.Item {...rest} name={[name,'end']} label="结束（HH:mm）" rules={[{required:true,pattern:/^\d{2}:\d{2}$/}]}><Input style={{width:110}} placeholder="24:00"/></Form.Item>
     <Form.Item {...rest} name={[name,'electric_price_cents']} label="电价（分/kWh）" rules={[{required:true}]}><InputNumber min={0} max={1000000} precision={0}/></Form.Item>
     <Form.Item {...rest} name={[name,'service_price_cents']} label="时段服务费（可选）"><InputNumber min={0} max={1000000} precision={0}/></Form.Item>
     <Button onClick={()=>remove(name)} disabled={fields.length===1} style={{marginTop:30}}>移除时段</Button>
    </Space>)}<Button onClick={()=>add({period:'',start:'',end:'',electric_price_cents:0})} disabled={fields.length>=48}>添加时段</Button></>}</Form.List>
   </Form>
  </Modal>
 </>;
}
