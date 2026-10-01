import { TABLE_PAGINATION } from '../utils/tablePagination';
import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Drawer, Modal, Popconfirm, Space, Table, Tag, message } from 'antd';
import { adminSession, apiDelete, apiGet, apiPost, apiPut } from '../api/client';
import SchemeEditor from './schemes/SchemeEditor';
import { Scheme, Template, blankScheme, modes } from './schemes/model';

export default function ChargingSchemes() {
  const [rows, setRows] = useState<Template[]>([]);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [editing, setEditing] = useState<{ template?: Template; scheme: Scheme }>();
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const loadedSession=useRef(adminSession.epoch());const loadVersion=useRef(0);
  const assertSession=()=>{if(loadedSession.current!==adminSession.epoch())throw new Error('账号已变化，请重新打开页面');};
  const load = async () => { const current=++loadVersion.current;try {assertSession(); const data = await apiGet<{ items: Template[]; permissions: string[] }>('/api/v1/admin/settings/charging-schemes'); if(current!==loadVersion.current)return;setRows(data.items); setPermissions(data.permissions); setError(''); } catch (e: any) { if(current===loadVersion.current)setError(e.message); } };
  useEffect(() => { void load();return()=>{loadVersion.current++;}; }, []);
  const save = async (scheme: Scheme) => {
    if (!editing || saving) return;
    assertSession();setSaving(true);
    try {
      if (editing.template) await apiPut(`/api/v1/admin/settings/charging-schemes/${editing.template.id}`, { scheme, expected_version: editing.template.version });
      else await apiPost('/api/v1/admin/settings/charging-schemes', { scheme });
      setEditing(undefined); message.success('完整充电方案已保存'); await load();
    } finally { setSaving(false); }
  };
  const setStatus = async (row: Template) => { try {assertSession(); await apiPost(`/api/v1/admin/settings/charging-schemes/${row.id}/status`, { expected_version: row.version, status: row.status === 'active' ? 'disabled' : 'active' }); await load(); } catch (e: any) { message.error(e.message); } };
  return <Space direction="vertical" style={{ width: '100%' }}>
    <Space><Button onClick={() => void load()}>刷新</Button>{permissions.includes('pricing.rule.create') && <Button type="primary" onClick={() => setEditing({ scheme: blankScheme() })}>新建充电方案</Button>}</Space>
    <Alert type="info" showIcon message="完整方案包含费率、套餐与展示配置。到站点或设备工作区应用时复制；编辑、停用或删除模板不改变已应用方案与订单快照。" />
    {error && <Alert type="error" message={error} />}
    <Table pagination={TABLE_PAGINATION} size="middle" rowKey="id" dataSource={rows} columns={[
      { title: '方案名称', render: (_, r) => r.scheme.name }, { title: '版本', dataIndex: 'version' },
      { title: '用户入口', render: (_, r) => [...new Set(r.scheme.packages.map(p => modes[p.mode]))].join('、') },
      { title: '套餐数', render: (_, r) => r.scheme.packages.length },
      { title: '状态', render: (_, r) => <Tag>{r.status === 'active' ? '启用' : '停用'}</Tag> },
      { title: '操作', render: (_, r) => <Space>
        {permissions.includes('pricing.rule.update') && <><Button onClick={() => setEditing({ template: r, scheme: structuredClone(r.scheme) })}>编辑与预览</Button><Button onClick={() => void setStatus(r)}>{r.status === 'active' ? '停用' : '启用'}</Button></>}
        {permissions.includes('pricing.rule.create') && <Button onClick={() => setEditing({ scheme: { ...structuredClone(r.scheme), name: `${r.scheme.name}（副本）` } })}>复制</Button>}
        {permissions.includes('pricing.rule.update') && <Popconfirm title="删除模板？已应用副本和订单不受影响。" onConfirm={async () => { try { assertSession();await apiDelete(`/api/v1/admin/settings/charging-schemes/${r.id}`, { expected_version: r.version }); await load(); } catch (e: any) { message.error(e.message); } }}><Button danger>删除</Button></Popconfirm>}
      </Space> },
    ]} />
    <Drawer width={1100} title={editing?.template ? '编辑充电方案' : '新建充电方案'} open={!!editing} maskClosable={!saving} closable={!saving} onClose={() => { if (!saving) Modal.confirm({ title: '放弃尚未保存的修改？', onOk: () => setEditing(undefined) }); }} destroyOnHidden>
      {editing && <SchemeEditor key={editing.template?.id || 'new'} value={editing.scheme} saving={saving} onSave={save} />}
    </Drawer>
  </Space>;
}
