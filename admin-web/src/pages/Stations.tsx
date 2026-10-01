import { useEffect, useRef, useState } from 'react';
import { useSearchParams } from 'react-router-dom';
import { Table, Space, Button, Drawer, Modal, Form, Input, InputNumber, Select, Tag, message } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';
import { LoadError } from '../components/LoadError';
import StationWorkspace, { stationStatuses, type StationRecord as Station } from './stations/StationWorkspace';
import { DEFAULT_PAGE_SIZE, TABLE_PAGINATION } from '../utils/tablePagination';

export default function StationsPage() {
  const [searchParams, setSearchParams] = useSearchParams();
  const [data, setData] = useState<Station[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [permissions,setPermissions] = useState<string[]>([]);
  const generation = useRef(0);
  const [query,setQuery]=useState({page:1,page_size:DEFAULT_PAGE_SIZE,keyword:'',status:''});
  const [total,setTotal]=useState(0);
  const [keyword,setKeyword]=useState('');
  const [status,setStatus]=useState('');
  const [workspace, setWorkspace] = useState<Station | null>(null);
  const [initialDeviceID, setInitialDeviceID] = useState<string | null>(null);
  const linkedStationID = searchParams.get('station_id');
  const linkedDeviceID = searchParams.get('device_id');
  const linkGeneration = useRef(0);

  useEffect(() => {
    const current = ++linkGeneration.current;
    if (!linkedStationID) return;
    const id = Number(linkedStationID);
    if (!/^\d+$/.test(linkedStationID) || !Number.isSafeInteger(id) || id <= 0) {
      message.error('站点 ID 无效'); return;
    }
    setWorkspace(null);
    void apiGet<Station>(`/api/v1/admin/stations/${id}`).then(station => {
      if (current !== linkGeneration.current) return;
      setInitialDeviceID(linkedDeviceID); setWorkspace(station);
    }).catch(cause => {
      if (current === linkGeneration.current) message.error(cause instanceof Error ? cause.message : '站点读取失败');
    });
    return () => { linkGeneration.current++; };
  }, [linkedStationID, linkedDeviceID]);

  const clearWorkspaceLink = () => {
    if (linkedStationID || linkedDeviceID) {
      const params = new URLSearchParams(searchParams);
      params.delete('station_id'); params.delete('device_id');
      setSearchParams(params, { replace: true });
    }
  };
  const showWorkspace = (station: Station) => {
    linkGeneration.current++; clearWorkspaceLink(); setInitialDeviceID(null); setWorkspace(station);
  };
  const closeWorkspace = () => { linkGeneration.current++; clearWorkspaceLink(); setWorkspace(null); setInitialDeviceID(null); };

  const load = async () => {
    const current = ++generation.current;
    setLoading(true); setError(''); setPermissions([]);
    try { const result = await apiGet<{items:Station[];permissions:string[];total:number}>('/api/v1/admin/stations',query); if(current === generation.current){setData(result.items);setPermissions(result.permissions);setTotal(result.total);} }
    catch(e:any) { if(current === generation.current){setData([]);setTotal(0);setError(e?.response?.data?.message || e.message || '站点读取失败');} }
    finally { if(current === generation.current)setLoading(false); }
  };
  useEffect(() => { load(); return () => { generation.current++; }; }, [query]);
  const create = () => {
    form.resetFields();
    form.setFieldsValue({status:'active'});
    setOpen(true);
  };
  const onSave = async () => {
    if(saving) return;
    try {
      const values = await form.validateFields();
      setSaving(true);
      await apiPost('/api/v1/admin/stations',values);
      message.success('已创建'); setOpen(false);
      setQuery({...query,page:1});
    } catch(e:any) { if(!e?.errorFields)message.error(e?.response?.data?.message || e.message || '保存失败'); }
    finally {setSaving(false);}
  };

  return (
    <div className="page-container">
      <div className="list-search-row"><Space wrap>
        <Input aria-label="站点关键词" placeholder="搜索名称或地址" maxLength={128} value={keyword} onChange={e=>setKeyword(e.target.value)} onPressEnter={()=>setQuery({...query,page:1,keyword,status})} allowClear />
        <Select aria-label="站点状态" value={status} onChange={setStatus} style={{width:140}} options={[{value:'',label:'全部状态'},{value:'active',label:'运营中'},{value:'disabled',label:'已停用'},{value:'construction',label:'建设中'}]} />
        <Button onClick={()=>setQuery({...query,page:1,keyword,status})}>查询</Button>
        <Button onClick={()=>{setKeyword('');setStatus('');setQuery({...query,page:1,keyword:'',status:''});}}>重置</Button>
      </Space>
        <Button type="primary" icon={<PlusOutlined />} disabled={!permissions.includes('station.create')} onClick={create}>新建</Button>
      </div>
      {error && <LoadError title="站点列表加载失败" detail={error} onRetry={() => void load()} />}
      <Table size="middle"
        rowKey="id"
        loading={loading}
        dataSource={data}
        scroll={{ x: 900 }}
        pagination={{...TABLE_PAGINATION,current:query.page,pageSize:query.page_size,total,showTotal:n=>`共 ${n} 个站点`,onChange:(page,page_size)=>setQuery({...query,page:page_size===query.page_size?page:1,page_size})}}
        columns={[
          { title: '名称', dataIndex: 'name', render: (name: string, station: Station) => <Button type="link" style={{ paddingInline: 0 }} onClick={() => showWorkspace(station)}>{name}</Button> },
          { title: '地址', dataIndex: 'address' },
          { title: '联系电话', dataIndex: 'contact_phone', render: (value?: string) => value || '—' },
          { title: '状态', dataIndex: 'status', width: 100, render:(value:string)=><Tag color={stationStatuses[value]?.color}>{stationStatuses[value]?.label || value}</Tag> },
          { title:'操作',key:'actions',width:100,render:(_:unknown,station:Station)=><Button type="link" onClick={() => showWorkspace(station)}>管理</Button> },
        ]}
      />
      <Drawer title={workspace ? `${workspace.name} · 站点管理` : '站点管理'} open={!!workspace}
        width="min(1120px, 100vw)" destroyOnClose onClose={closeWorkspace}>
        {workspace && <StationWorkspace key={workspace.id} station={workspace} permissions={permissions} initialDeviceId={initialDeviceID} onSaved={updated => { setWorkspace(current => current?.id === updated.id ? updated : current); void load(); }} />}
      </Drawer>
      <Modal title="新建站点" open={open} confirmLoading={saving} closable={!saving} maskClosable={!saving} onCancel={() => {if(!saving)setOpen(false);}} onOk={onSave} okText="保存" cancelText="取消">
        <Form form={form} layout="vertical">
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace:true }]}><Input maxLength={128} /></Form.Item>
          <Form.Item name="address" label="地址"><Input maxLength={255} /></Form.Item>
          <Form.Item name="longitude" label="经度" rules={[{ required: true }]}><InputNumber min={-180} max={180} precision={8} style={{width:"100%"}} /></Form.Item>
          <Form.Item name="latitude" label="纬度" rules={[{ required: true }]}><InputNumber min={-90} max={90} precision={8} style={{width:"100%"}} /></Form.Item>
          <Form.Item name="contact_phone" label="联系电话"><Input maxLength={32} /></Form.Item>
          <Form.Item name="status" label="状态" rules={[{required:true}]}><Select options={[{value:'active',label:'运营中'},{value:'disabled',label:'已停用'},{value:'construction',label:'建设中'}]} /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
}
