import { Table, Typography, Tag, Space, Button, Input, Select } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';
import { apiGet } from '../api/client';
import DeviceImport from './DeviceImport';

interface Device {
  id: number; device_id: string; station_id?: number; station_name?: string;
  station_code?: string; vendor_id?: number; model?: string; status: string; install_at?: string;
}
const statuses: Record<string, { label: string; color: string }> = {
  enabled: {label:'已启用',color:'green'}, disabled:{label:'已停用',color:'default'},
  retired:{label:'已退役',color:'red'}, fault:{label:'故障',color:'volcano'},
};
export default function DevicesPage() {
  const [data,setData]=useState<Device[]>([]);
  const [loading,setLoading]=useState(false);
  const [error,setError]=useState('');
  const [permissions,setPermissions]=useState<string[]>([]);
  const [total,setTotal]=useState(0);
  const [keyword,setKeyword]=useState('');
  const [status,setStatus]=useState('');
  const [query,setQuery]=useState({page:1,page_size:20,keyword:'',status:''});
  const generation=useRef(0);
  const load=async()=>{
    const current=++generation.current;
    setLoading(true);setError('');setData([]);setTotal(0);setPermissions([]);
    try {
      const result=await apiGet<{items:Device[];total:number;permissions:string[]}>('/api/v1/admin/devices',query);
      if(current===generation.current){setData(result.items);setTotal(result.total);setPermissions(result.permissions);}
    } catch(e:any){if(current===generation.current)setError(e?.response?.data?.message || e.message || '设备读取失败');}
    finally{if(current===generation.current)setLoading(false);}
  };
  useEffect(()=>{load();return()=>{generation.current++;};},[query]);
  const search=()=>setQuery({...query,page:1,keyword,status});
  return <div className="page-container">
    <Space style={{marginBottom:12}}>
      <Typography.Title level={3} style={{margin:0}}>设备</Typography.Title>
      <Button icon={<ReloadOutlined/>} onClick={load}>刷新</Button>
      {permissions.includes('device.import') && <DeviceImport onComplete={()=>setQuery({...query,page:1})}/>}
    </Space>
    <div style={{marginBottom:12}}><Space wrap>
      <Input aria-label="设备关键词" placeholder="设备编号、型号、站点名称或编码" style={{width:320}} maxLength={128} value={keyword} onChange={e=>setKeyword(e.target.value)} onPressEnter={search} allowClear/>
      <Select aria-label="设备状态" style={{width:140}} value={status} onChange={setStatus} options={[{value:'',label:'全部状态'},...Object.entries(statuses).map(([value,s])=>({value,label:s.label}))]}/>
      <Button onClick={search}>查询</Button>
      <Button onClick={()=>{setKeyword('');setStatus('');setQuery({...query,page:1,keyword:'',status:''});}}>重置</Button>
    </Space></div>
    {error && <div role="alert" style={{color:'#cf1322',marginBottom:12}}>{error}</div>}
    <Table<Device> rowKey="id" loading={loading} dataSource={data}
      pagination={{current:query.page,pageSize:query.page_size,total,showSizeChanger:true,pageSizeOptions:[10,20,50,100],showTotal:n=>`共 ${n} 台设备`,onChange:(page,page_size)=>setQuery({...query,page:page_size===query.page_size?page:1,page_size})}}
      columns={[
        {title:'设备编号',dataIndex:'device_id'},
        {title:'型号',dataIndex:'model',render:(v?:string)=>v || '-'},
        {title:'站点',key:'station',render:(_,d)=>d.station_name ? `${d.station_name}（${d.station_code}）` : d.station_id ? `站点 #${d.station_id}` : '未分配'},
        {title:'厂商 ID',dataIndex:'vendor_id',render:(v?:number)=>v ?? '-'},
        {title:'管理状态',dataIndex:'status',render:(s:string)=><Tag color={statuses[s]?.color || 'default'}>{statuses[s]?.label || s}</Tag>},
        {title:'安装时间',dataIndex:'install_at',render:(v?:string)=>v ? new Date(v).toLocaleString() : '-'},
      ]}/>
  </div>;
}
