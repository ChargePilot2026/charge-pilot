import { TABLE_PAGINATION } from '../utils/tablePagination';
import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Form, Input, Modal, Select, Space, Table, Tag, message } from 'antd';
import { adminSession, apiGet, apiPost } from '../api/client';
import { isChargeUserID } from '../utils/chargeUserID';

type Card = { id:number; card_no:string; user_id:string; status:string };
const labels:Record<string,string>={active:'正常',lost:'挂失',disabled:'禁用',unbound:'已解绑'};
export default function OnlineCards(){
 const [rows,setRows]=useState<Card[]>([]),[permissions,setPermissions]=useState<string[]>([]),[error,setError]=useState(''),[busy,setBusy]=useState(false),[open,setOpen]=useState(false),[selected,setSelected]=useState<Card>();
 const [form]=Form.useForm<{card_no?:string;user_id?:string;status?:string;reason:string}>(); const generation=useRef(0),session=useRef<string|null>(null);
 async function load(){const current=++generation.current;try{const d=await apiGet<{items:Card[];permissions:string[]}>('/api/v1/admin/online-cards');if(current===generation.current){setRows(d.items);setPermissions(d.permissions);setError('');}}catch(e:any){if(current===generation.current)setError(e.message);}}
 useEffect(()=>{void load();return()=>{generation.current++;};},[]);
 function edit(card?:Card){setSelected(card);session.current=adminSession.epoch();form.resetFields();form.setFieldsValue(card?{status:card.status}:{});setOpen(true);}
 async function save(){if(busy)return;try{const v=await form.validateFields();if(session.current!==adminSession.epoch())throw new Error('账号已变化，请重新打开');setBusy(true);await apiPost(selected?`/api/v1/admin/online-cards/${selected.id}/status`:'/api/v1/admin/online-cards/bind',v);message.success('卡信息已保存');setOpen(false);await load();}catch(e:any){if(!e.errorFields)setError(e.message);}finally{setBusy(false);}}
 return <div className="page-container"><Alert type="info" message="在线卡绑定用户钱包；一个用户可绑定多张卡。解绑只解除绑定，余额仍属于原用户。"/>
 <Space style={{margin:'16px 0'}}>{permissions.includes('online_card.manage')&&<Button type="primary" onClick={()=>edit()}>核验并绑定卡</Button>}<Button onClick={()=>void load()}>刷新</Button></Space>{error&&<Alert type="error" message={error}/>}<Table pagination={TABLE_PAGINATION} size="middle" rowKey="id" dataSource={rows} columns={[{title:'卡号',dataIndex:'card_no'},{title:'用户 ID',dataIndex:'user_id'},{title:'状态',render:(_,r)=><Tag>{labels[r.status]}</Tag>},{title:'操作',render:(_,r)=>permissions.includes('online_card.manage')&&r.status!=='unbound'?<Button onClick={()=>edit(r)}>修改状态</Button>:null}]}/>
 <Modal title={selected?'修改在线卡状态':'核验并绑定在线卡'} open={open} confirmLoading={busy} onOk={()=>void save()} onCancel={()=>{if(!busy)setOpen(false);}}><Form form={form} layout="vertical">{selected?<><p>{selected.card_no} · 用户 {selected.user_id}</p><Form.Item name="status" label="状态" rules={[{required:true}]}><Select options={Object.entries(labels).map(([value,label])=>({value,label}))}/></Form.Item></>:<><Form.Item name="card_no" label="卡号" rules={[{required:true,whitespace:true,max:64}]}><Input/></Form.Item><Form.Item name="user_id" label="用户 ID" normalize={(value:string)=>value.trim()} rules={[{required:true,message:'请输入用户 ID'},{validator:(_:unknown,value?:string)=>!value||isChargeUserID(value)?Promise.resolve():Promise.reject(new Error('请输入有效的纯数字用户 ID'))}]}><Input inputMode="numeric" placeholder="完整用户 ID"/></Form.Item></>}<Form.Item name="reason" label="核验 / 处理依据" rules={[{required:true,whitespace:true,max:500}]}><Input.TextArea rows={3}/></Form.Item></Form></Modal></div>;
}
