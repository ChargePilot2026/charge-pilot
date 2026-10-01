import { Table, Typography, Tag, Space, Button, Input, Select, Popconfirm, Alert, Tooltip, message } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';
import { useNavigate } from 'react-router-dom';
import { apiGet, apiPut, adminSession } from '../api/client';
import DeviceImport from './DeviceImport';
import DeviceCreate from './DeviceCreate';
import DeviceEdit from './DeviceEdit';
import { LoadError } from '../components/LoadError';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../utils/tablePagination';
import { portStatusText, signalStrengthText } from '../utils/deviceTelemetry';

interface Device {
  id: number; device_id: string; station_id?: number; station_name?: string;
  vendor_id?: number; vendor_name?: string; last_heartbeat_at?: string; runtime_available?: boolean; model?: string; status: string;
  signal_strength?: number | null; signal_at?: string | null;
  ports?: { port_no: number; status_code: number | null; status_at: string | null }[] | null;
}
const statuses: Record<string, { label: string; color: string }> = {
  enabled: {label:'已启用',color:'green'}, disabled:{label:'已禁用',color:'red'},
  retired:{label:'已退役',color:'red'}, fault:{label:'故障',color:'volcano'},
};
const portStatusColors: Record<number, string> = { 0: 'green', 1: 'blue', 3: 'red', 4: 'orange' };
const sampledAt = (value: string | null | undefined) => value ? `采样时间：${new Date(value).toLocaleString()}` : '采样时间未记录';
const deviceSignal = (device: Device) => device.runtime_available === false ? '暂不可读取'
  : device.signal_strength == null ? '—'
    : <Tooltip title={sampledAt(device.signal_at)}><span>{signalStrengthText(device.signal_strength)}</span></Tooltip>;
const devicePorts = (device: Device) => device.runtime_available === false ? '暂不可读取'
  : !device.ports?.length ? '—'
    : <Space size={[0, 4]} wrap style={{ maxWidth: 320 }}>
      {[...device.ports].sort((a, b) => a.port_no - b.port_no).map(port => {
        const state = port.status_code == null ? '未上报' : portStatusText(port.status_code);
        const description = `端口 ${port.port_no}：${state} · 最近上报时间：${port.status_at ? new Date(port.status_at).toLocaleString() : '—'}`;
        return <Tooltip key={port.port_no} title={description}>
          <Tag color={port.status_code == null ? 'default' : portStatusColors[port.status_code] || 'default'}
            aria-label={description} tabIndex={0}>{port.port_no}</Tag>
        </Tooltip>;
      })}
    </Space>;
export default function DevicesPage({ station, embedded = false, onConfigure }: {
  station?: { id: number; name: string; status: string };
  embedded?: boolean;
  onConfigure?: (deviceID: string) => void;
} = {}) {
  const navigate = useNavigate();
  const [data,setData]=useState<Device[]>([]);
  const [loading,setLoading]=useState(false);
  const [error,setError]=useState('');
  const [permissions,setPermissions]=useState<string[]>([]);
  const [total,setTotal]=useState(0);
  const [keyword,setKeyword]=useState('');
  const [status,setStatus]=useState('');
  const [query,setQuery]=useState({page:1,page_size:DEFAULT_PAGE_SIZE,keyword:'',status:''});
  const generation=useRef(0);
  const [operating,setOperating]=useState<string>();
  const [editing,setEditing]=useState<string>();
  const operatingRef=useRef(false);
  const changeStatus=async(device:Device)=>{
    if(operatingRef.current)return;
    operatingRef.current=true;setOperating(device.device_id);
    const current=generation.current, session=adminSession.epoch();
    try{
      await apiPut('/api/v1/admin/devices/'+encodeURIComponent(device.device_id)+'/status',{status:device.status==='enabled'?'disabled':'enabled'});
      if(current!==generation.current||session!==adminSession.epoch())return;
      message.success(device.status==='enabled'?'设备已禁用':'设备已启用');
      await load();
    }catch(e:any){if(current===generation.current&&session===adminSession.epoch())message.error(e?.response?.data?.message||e.message||'运营状态保存失败');}
    finally{operatingRef.current=false;setOperating(undefined);}
  };
  const load=async()=>{
    const current=++generation.current;
    setLoading(true);setError('');setData([]);setTotal(0);setPermissions([]);
    try {
      const result=await apiGet<{items:Device[];total:number;permissions:string[]}>('/api/v1/admin/devices',{...query, station_id:station?.id});
      if(current===generation.current){setData(result.items);setTotal(result.total);setPermissions(result.permissions);}
    } catch(e:any){if(current===generation.current)setError(e?.response?.data?.message || e.message || '设备读取失败');}
    finally{if(current===generation.current)setLoading(false);}
  };
  useEffect(()=>{load();return()=>{generation.current++;};},[query,station?.id]);
  const search=()=>setQuery({...query,page:1,keyword,status});
  const openStation = (device: Device, configure = false) => {
    if (!device.station_id) return;
    const params = new URLSearchParams({ station_id: String(device.station_id) });
    if (configure) params.set('device_id', device.device_id);
    navigate(`/stations?${params.toString()}`);
  };
  const createActions = <>
    {permissions.includes('device.import') && (!station || station.status === 'active') && <DeviceCreate station={station} canReadStations={permissions.includes('station.read')} onComplete={()=>setQuery({...query,page:1})}/>}
    {permissions.includes('device.import') && !station && <DeviceImport onComplete={()=>setQuery({...query,page:1})}/>}
  </>;
  return <div className={embedded ? 'station-devices' : 'page-container'}>
    {embedded && <Space wrap style={{marginBottom:12}}>
      <Typography.Title level={5} style={{margin:0}}>{station ? '本站设备' : '设备'}</Typography.Title>
      <Button icon={<ReloadOutlined/>} onClick={load}>刷新</Button>
      {createActions}
    </Space>}
    <div className="list-search-row"><Space wrap>
      <Input aria-label="设备关键词" placeholder={station ? '设备编号或型号' : '设备编号、型号或站点名称'} style={{width:320,maxWidth:'100%'}} maxLength={128} value={keyword} onChange={e=>setKeyword(e.target.value)} onPressEnter={search} allowClear/>
      <Select aria-label="设备状态" style={{width:140}} value={status} onChange={setStatus} options={[{value:'',label:'全部状态'},...Object.entries(statuses).map(([value,s])=>({value,label:s.label}))]}/>
      <Button onClick={search}>查询</Button>
      <Button onClick={()=>{setKeyword('');setStatus('');setQuery({...query,page:1,keyword:'',status:''});}}>重置</Button>
    </Space>{!embedded && <Space wrap>{createActions}</Space>}</div>
    {error && <LoadError title="设备列表加载失败" detail={error} onRetry={() => void load()} />}
    {!loading&&data.some(d=>d.runtime_available===false)&&<Alert type="warning" showIcon style={{marginBottom:12}} message="部分设备的厂商及心跳信息暂不可读取，请刷新重试。"/>}
    <Table<Device> size="middle" rowKey="id" loading={loading} dataSource={data} scroll={{x:'max-content'}}
      pagination={{...TABLE_PAGINATION,current:query.page,pageSize:query.page_size,total,showTotal:n=>`共 ${n} 台设备`,onChange:(page,page_size)=>setQuery({...query,page:page_size===query.page_size?page:1,page_size})}}
      columns={[
        {title:'设备编号',dataIndex:'device_id'},
        {title:'型号',dataIndex:'model',render:(v?:string)=>v || '-'},
        ...(!station ? [{title:'站点',key:'station',render:(_:unknown,d:Device)=>d.station_id && permissions.includes('station.read')
          ? <Button type="link" onClick={() => openStation(d)}>{d.station_name || `站点 #${d.station_id}`}</Button>
          : d.station_name || (d.station_id ? `站点 #${d.station_id}` : '未分配')}] : []),
        {title:'厂商',key:'vendor',render:(_:unknown,d:Device)=>d.runtime_available===false?'暂不可读取':d.vendor_name||'未登记'},
        {title:'运营状态',dataIndex:'status',render:(s:string)=><Tag color={statuses[s]?.color || 'default'}>{statuses[s]?.label || s}</Tag>},
        {title:'信号强度',key:'signal',render:(_:unknown,d:Device)=>deviceSignal(d)},
        {title:'端口状态',key:'ports',width:320,render:(_:unknown,d:Device)=>devicePorts(d)},
        {title:'最后在线时间',key:'heartbeat',render:(_:unknown,d:Device)=>d.runtime_available===false?'暂不可读取':d.last_heartbeat_at?new Date(d.last_heartbeat_at).toLocaleString():'尚无心跳'},
        ...(permissions.includes('device.operate') || permissions.includes('pricing.read') && (onConfigure || permissions.includes('station.read')) ? [{title:'操作',key:'configuration',render:(_:unknown,d:Device)=><Space>
          {permissions.includes('device.operate')&&<Button type="link" onClick={() => setEditing(d.device_id)}>编辑</Button>}
          {permissions.includes('pricing.read')&&(onConfigure||permissions.includes('station.read'))&&<Button type="link" disabled={!d.station_id}
          onClick={() => onConfigure ? onConfigure(d.device_id) : openStation(d, true)}>计费与套餐</Button>}
          {permissions.includes('device.operate')&&['enabled','disabled'].includes(d.status)&&<Popconfirm title={d.status==='enabled'?'禁用此设备？':'启用此设备？'} description={d.status==='enabled'?'禁用后不接受新充电和刷卡加时，已有订单可继续充电并正常结束。':'启用后，在线且空闲的端口可以发起充电。'} onConfirm={()=>changeStatus(d)}>
            <Button type="link" danger={d.status==='enabled'} disabled={!!operating} loading={operating===d.device_id}>{d.status==='enabled'?'禁用':'启用'}</Button>
          </Popconfirm>}
        </Space>}] : []),
      ]}/>
    {editing && <DeviceEdit key={editing} deviceID={editing} onClose={() => setEditing(undefined)} onComplete={() => void load()} />}
  </div>;
}
