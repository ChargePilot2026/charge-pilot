import { useEffect, useState } from 'react';
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Space, Table, Tag, message } from 'antd';
import { apiGet, apiPost, apiPut } from '../api/client';

type Offer = { id:number; station_id:number; station_name:string; code:string; name:string; mode:'amount'|'package'; price_cents:number; duration_minutes:number; status:'active'|'disabled'; version:number };

export default function ChargeOffers(){
 const [items,setItems]=useState<Offer[]>([]);
 const [permissions,setPermissions]=useState<string[]>([]);
 const [loading,setLoading]=useState(false);
 const [saving,setSaving]=useState(false);
 const [editing,setEditing]=useState<Offer|null>(null);
 const [open,setOpen]=useState(false);
 const [error,setError]=useState('');
 const [form]=Form.useForm();
 const mode=Form.useWatch('mode',form);
 const load=async()=>{setLoading(true);try{const result=await apiGet<{items:Offer[];permissions:string[]}>('/api/v1/admin/settings/charge-offers');setItems(result.items);setPermissions(result.permissions||[]);}catch(e:any){message.error(e.message);}finally{setLoading(false);}};
 useEffect(()=>{void load();},[]);
 const canCreate=permissions.includes('pricing.rule.create');
 const canUpdate=permissions.includes('pricing.rule.update');
 const edit=(offer?:Offer)=>{setEditing(offer||null);setError('');form.resetFields();form.setFieldsValue(offer||{mode:'amount',status:'active',duration_minutes:0});setOpen(true);};
 const save=async()=>{
  try{
   const values=await form.validateFields();setSaving(true);setError('');
   const body={...values,duration_minutes:values.mode==='amount'?0:values.duration_minutes,expected_version:editing?.version||0};
   if(editing)await apiPut(`/api/v1/admin/settings/charge-offers/${editing.id}`,body);
   else await apiPost('/api/v1/admin/settings/charge-offers',body);
   message.success('充电方案已保存');setOpen(false);await load();
  }catch(e:any){if(!e.errorFields)setError(e.message||'保存失败');}finally{setSaving(false);}
 };
 return <>
  <Space style={{marginBottom:12}}><Button onClick={()=>void load()} loading={loading}>刷新方案</Button>{canCreate&&<Button type="primary" onClick={()=>edit()}>新增方案</Button>}</Space>
  <Alert type="info" showIcon message="金额充电按实际费用消耗固定金额；时长套餐按已使用秒数结算，提前结束的未使用时长原路退款。修改方案不改变已付款订单的快照。" style={{marginBottom:12}}/>
  <Table rowKey="id" dataSource={items} loading={loading} columns={[
   {title:'站点',render:(_:unknown,row:Offer)=>`${row.station_name||row.station_id}（${row.station_id}）`},
   {title:'方案',render:(_:unknown,row:Offer)=><>{row.name}<div>{row.code}</div></>},
   {title:'类型',dataIndex:'mode',render:(value:string)=>value==='amount'?'金额充电':'时长套餐'},
   {title:'价格',dataIndex:'price_cents',render:(value:number)=>`¥${(value/100).toFixed(2)}`},
   {title:'时长',dataIndex:'duration_minutes',render:(value:number)=>value?`${value} 分钟`:'按金额上限'},
   {title:'状态',dataIndex:'status',render:(value:string)=><Tag color={value==='active'?'green':'default'}>{value==='active'?'可售':'已停用'}</Tag>},
   ...(canUpdate?[{title:'操作',render:(_:unknown,row:Offer)=><Button type="link" onClick={()=>edit(row)}>编辑</Button>}]:[]),
  ]}/>
  <Modal title={editing?'编辑充电方案':'新增充电方案'} open={open} onCancel={()=>setOpen(false)} onOk={()=>void save()} confirmLoading={saving} okText="保存">
   {error&&<Alert type="error" showIcon message={error} style={{marginBottom:12}}/>}
   <Form form={form} layout="vertical">
    <Form.Item name="station_id" label="可售站点 ID" rules={[{required:true}]}><InputNumber min={1} precision={0} disabled={!!editing} style={{width:'100%'}}/></Form.Item>
    <Form.Item name="code" label="方案编码" rules={[{required:true,whitespace:true,max:64}]}><Input maxLength={64}/></Form.Item>
    <Form.Item name="name" label="小程序展示名称" rules={[{required:true,whitespace:true,max:128}]}><Input maxLength={128}/></Form.Item>
    <Form.Item name="mode" label="充电方式" rules={[{required:true}]}><Select options={[{value:'amount',label:'金额充电'},{value:'package',label:'时长套餐'}]}/></Form.Item>
    <Form.Item name="price_cents" label="支付价格（分）" rules={[{required:true}]}><InputNumber min={1} max={1000000} precision={0} style={{width:'100%'}}/></Form.Item>
    {mode==='package'&&<Form.Item name="duration_minutes" label="套餐时长（分钟）" rules={[{required:true}]}><InputNumber min={1} max={600} precision={0} style={{width:'100%'}}/></Form.Item>}
    <Form.Item name="status" label="销售状态" rules={[{required:true}]}><Select options={[{value:'active',label:'可售'},{value:'disabled',label:'停用'}]}/></Form.Item>
   </Form>
  </Modal>
 </>;
}
