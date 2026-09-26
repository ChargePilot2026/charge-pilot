import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Input, Modal, Table } from 'antd';
import { apiGet, apiPost } from '../api/client';
interface Risk {request_id:string;user_id:string;amount_cents:number;reason:string|null;created_at:string}
export default function WalletRisks(){
 const [page,setPage]=useState(1),[reload,setReload]=useState(0),[items,setItems]=useState<Risk[]>([]),[total,setTotal]=useState(0);
 const [loading,setLoading]=useState(false),[error,setError]=useState(''),[notice,setNotice]=useState('');
 const [selected,setSelected]=useState<Risk|null>(null),[approved,setApproved]=useState(true),[comment,setComment]=useState(''),[submitError,setSubmitError]=useState(''),[busy,setBusy]=useState(false);
 const flight=useRef(false),session=useRef(''),alive=useRef(true);
 useEffect(()=>{alive.current=true;return()=>{alive.current=false;};},[]);
 useEffect(()=>{let current=true;setLoading(true);setError('');setItems([]);
  apiGet<{items:Risk[];total:number}>(`/api/v1/admin/billing/wallet-risks?page=${page}&page_size=20`).then(v=>{if(current){setItems(v.items);setTotal(v.total);}}).catch(e=>{if(current)setError(e.message||'风控队列读取失败');}).finally(()=>{if(current)setLoading(false);});return()=>{current=false;};
 },[page,reload]);
 const open=(item:Risk,approve:boolean)=>{session.current=localStorage.getItem('cp_token')||'';setSelected(item);setApproved(approve);setComment('');setSubmitError('');};
 const submit=async()=>{
  if(flight.current||!selected)return;
  if(!comment.trim()){setSubmitError('请填写审核依据');return;}
  if(session.current!==localStorage.getItem('cp_token')){setSubmitError('登录账号已变化，请关闭后刷新');return;}
  flight.current=true;setBusy(true);setSubmitError('');
  try{await apiPost(`/api/v1/admin/billing/wallet-risks/${selected.request_id}/review`,{approved,comment:comment.trim()});
   if(alive.current&&session.current===localStorage.getItem('cp_token')){setSelected(null);setNotice(approved?'已通过审核并进入退款处理，实际到账以退款结果为准。':'已拒绝该退款申请，审核意见已保存。');setReload(v=>v+1);}
  }catch(e:any){if(alive.current&&session.current===localStorage.getItem('cp_token'))setSubmitError(e.message||'审核结果未确认，请用相同决定和意见重试');}
  finally{flight.current=false;if(alive.current)setBusy(false);}
 };
 return <div>
  <Alert type="info" showIcon message="钱包退款风控待审" description="通过后将重新核实可退余额并生成原路退款任务。此处审核退款申请，钱包解冻需单独处置。" style={{marginBottom:12}}/>
  {notice&&<Alert type="success" message={notice} style={{marginBottom:12}}/>}
  {error&&<Alert type="error" message={error} style={{marginBottom:12}}/>}
  <Button onClick={()=>setReload(v=>v+1)} disabled={loading||busy} style={{marginBottom:12}}>刷新队列</Button>
  <Table<Risk> rowKey="request_id" dataSource={items} loading={loading} scroll={{x:1100}} pagination={{current:page,pageSize:20,total,showSizeChanger:false,onChange:setPage}} columns={[
   {title:'申请编号',dataIndex:'request_id',width:290},{title:'用户 ID',dataIndex:'user_id',width:120},
   {title:'申请金额',width:120,render:(_,r)=>`¥${(r.amount_cents/100).toFixed(2)}`},{title:'申请原因',dataIndex:'reason',width:180},
   {title:'申请时间',width:200,render:(_,r)=>new Date(r.created_at).toLocaleString()},
   {title:'审核',width:180,fixed:'right',render:(_,r)=><><Button type="link" disabled={busy} onClick={()=>open(r,true)}>通过</Button><Button type="link" danger disabled={busy} onClick={()=>open(r,false)}>拒绝</Button></>}
  ]}/>
  <Modal title={approved?'通过钱包退款审核':'拒绝钱包退款申请'} open={!!selected} confirmLoading={busy} onOk={submit} onCancel={()=>{if(!busy)setSelected(null);}} maskClosable={!busy} closable={!busy} cancelButtonProps={{disabled:busy}}>
   <p>{selected?.request_id} · ¥{((selected?.amount_cents||0)/100).toFixed(2)}</p>
   <Input.TextArea aria-label="审核依据" value={comment} onChange={e=>setComment(e.target.value)} maxLength={255} rows={4} disabled={busy} placeholder="填写核实情况和审核依据"/>
   {submitError&&<Alert type="error" message={submitError} style={{marginTop:12}}/>}
  </Modal>
 </div>;
}
