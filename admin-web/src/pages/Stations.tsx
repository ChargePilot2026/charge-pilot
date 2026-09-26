import { useEffect, useRef, useState } from 'react';
import { Table, Typography, Space, Button, Modal, Form, Input, InputNumber, Select, message } from 'antd';
import { PlusOutlined, ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost, http } from '../api/client';

const { Title } = Typography;

interface Station {
  id: number; code: string; name: string;
  address?: string; longitude: number; latitude: number;
  status: string; open_hours?: string; contact_phone?: string;
}

export default function StationsPage() {
  const [data, setData] = useState<Station[]>([]);
  const [loading, setLoading] = useState(false);
  const [open, setOpen] = useState(false);
  const [form] = Form.useForm();
  const [editing, setEditing] = useState<Station | null>(null);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [permissions,setPermissions] = useState<string[]>([]);
  const generation = useRef(0);
  const [query,setQuery]=useState({page:1,page_size:20,keyword:'',status:''});
  const [total,setTotal]=useState(0);
  const [keyword,setKeyword]=useState('');
  const [status,setStatus]=useState('');

  const load = async () => {
    const current = ++generation.current;
    setLoading(true); setError(''); setPermissions([]);
    try { const result = await apiGet<{items:Station[];permissions:string[];total:number}>('/api/v1/admin/stations',query); if(current === generation.current){setData(result.items);setPermissions(result.permissions);setTotal(result.total);} }
    catch(e:any) { if(current === generation.current)setError(e?.response?.data?.message || e.message || '站点读取失败'); }
    finally { if(current === generation.current)setLoading(false); }
  };
  useEffect(() => { load(); return () => { generation.current++; }; }, [query]);
  const edit = (station:Station | null) => {
    setEditing(station); form.resetFields();
    form.setFieldsValue(station ? {...station,address:station.address || '',open_hours:station.open_hours || '',contact_phone:station.contact_phone || ''} : {status:'active'});
    setOpen(true);
  };
  const onSave = async () => {
    if(saving) return;
    try {
      const values = await form.validateFields();
      setSaving(true);
      if(editing) { const {code,...update}=values; await http.put('/api/v1/admin/stations/'+editing.id,update); }
      else await apiPost('/api/v1/admin/stations',values);
      message.success(editing ? '已保存' : '已创建'); setOpen(false); if(editing)await load();else setQuery({...query,page:1});
    } catch(e:any) { if(!e?.errorFields)message.error(e?.response?.data?.message || e.message || '保存失败'); }
    finally {setSaving(false);}
  };

  return (
    <div className="page-container">
      <Space style={{ marginBottom: 12 }}>
        <Title level={3} style={{ margin: 0 }}>站点</Title>
        <Button icon={<ReloadOutlined />} onClick={load}>刷新</Button>
        <Button type="primary" icon={<PlusOutlined />} disabled={!permissions.includes('station.create')} onClick={() => edit(null)}>新建</Button>
      </Space>
      <Space wrap style={{marginBottom:12}}>
        <Input aria-label="站点关键词" placeholder="搜索编码、名称或地址" maxLength={128} value={keyword} onChange={e=>setKeyword(e.target.value)} onPressEnter={()=>setQuery({...query,page:1,keyword,status})} allowClear />
        <Select aria-label="站点状态" value={status} onChange={setStatus} style={{width:140}} options={[{value:'',label:'全部状态'},{value:'active',label:'运营中'},{value:'disabled',label:'已停用'},{value:'construction',label:'建设中'}]} />
        <Button onClick={()=>setQuery({...query,page:1,keyword,status})}>查询</Button>
        <Button onClick={()=>{setKeyword('');setStatus('');setQuery({...query,page:1,keyword:'',status:''});}}>重置</Button>
      </Space>
      {error && <div role="alert" style={{color:"#cf1322",marginBottom:12}}>{error}</div>}
      <Table
        rowKey="id"
        loading={loading}
        dataSource={data}
        pagination={{current:query.page,pageSize:query.page_size,total,showSizeChanger:true,pageSizeOptions:[10,20,50,100],showTotal:n=>`共 ${n} 个站点`,onChange:(page,page_size)=>setQuery({...query,page:page_size===query.page_size?page:1,page_size})}}
        columns={[
          { title: '编码', dataIndex: 'code', width: 120 },
          { title: '名称', dataIndex: 'name' },
          { title: '地址', dataIndex: 'address' },
          { title: '经度', dataIndex: 'longitude', width: 120 },
          { title: '纬度', dataIndex: 'latitude', width: 120 },
          { title: '状态', dataIndex: 'status', width: 100, render:(value:string)=>({active:'运营中',disabled:'已停用',construction:'建设中'}[value] || value) },
          { title:'操作',key:'actions',render:(_:unknown,station:Station)=><Button disabled={!permissions.includes('station.update')} onClick={()=>edit(station)}>编辑</Button> },
        ]}
      />
      <Modal title={editing ? "编辑站点" : "新建站点"} open={open} confirmLoading={saving} closable={!saving} maskClosable={!saving} onCancel={() => {if(!saving)setOpen(false);}} onOk={onSave} okText="保存" cancelText="取消">
        <Form form={form} layout="vertical">
          <Form.Item name="code" label="编码" rules={[{ required: true }]}><Input maxLength={64} disabled={!!editing} /></Form.Item>
          <Form.Item name="name" label="名称" rules={[{ required: true, whitespace:true }]}><Input maxLength={128} /></Form.Item>
          <Form.Item name="address" label="地址"><Input maxLength={255} /></Form.Item>
          <Form.Item name="longitude" label="经度" rules={[{ required: true }]}><InputNumber min={-180} max={180} precision={8} style={{width:"100%"}} /></Form.Item>
          <Form.Item name="latitude" label="纬度" rules={[{ required: true }]}><InputNumber min={-90} max={90} precision={8} style={{width:"100%"}} /></Form.Item>
          <Form.Item name="open_hours" label="营业时间"><Input maxLength={64} /></Form.Item>
          <Form.Item name="contact_phone" label="联系电话"><Input maxLength={32} /></Form.Item>
          <Form.Item name="status" label="状态" rules={[{required:true}]}><Select options={[{value:'active',label:'运营中'},{value:'disabled',label:'已停用'},{value:'construction',label:'建设中'}]} /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
}