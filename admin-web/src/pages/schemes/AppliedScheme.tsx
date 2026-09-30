import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Drawer, Popconfirm, Select, Space, Tag, Typography, message } from 'antd';
import { adminSession, apiGet, apiPost } from '../../api/client';
import { Scheme, Template } from './model';
import SchemeEditor from './SchemeEditor';
import DeviceCapabilities from './DeviceCapabilities';
import SchemeDetails from './SchemeDetails';
type Effective = { scheme: Scheme | null; version: number; inherited: boolean; permissions?: string[] };
export default function AppliedScheme({ station, deviceId, onDeviceChange }: { station: { id: number; name: string }; deviceId: string | null; onDeviceChange: (id: string | null) => void }) {
  const [effective, setEffective] = useState<Effective>();
  const [templates, setTemplates] = useState<Template[]>([]);
  const [devices, setDevices] = useState<{ device_id: string }[]>([]);
  const [selected, setSelected] = useState<number>();
  const [editing, setEditing] = useState<Scheme>();
  const [error, setError] = useState('');
  const [busy, setBusy] = useState(false);
  const [permissions, setPermissions] = useState<string[]>([]);
  const epoch = useRef(0);const loadedSession=useRef(adminSession.epoch());
  const assertSession=()=>{if(loadedSession.current!==adminSession.epoch())throw new Error('账号已变化，请重新打开页面');};
  const intent = useRef<{ payload: string; request: string }>();
  const load = async () => {
    const current = ++epoch.current;
    setEffective(undefined); setError(''); setSelected(undefined);
    try {assertSession();
      const [data, list, devs] = await Promise.all([
        apiGet<Effective>(`/api/v1/admin/stations/${station.id}/charging-scheme`, deviceId ? { device_id: deviceId } : {}),
        apiGet<{ items: Template[]; permissions: string[] }>('/api/v1/admin/settings/charging-schemes'),
        apiGet<{ items: { device_id: string }[] }>('/api/v1/admin/devices', { station_id: station.id, page_size: 100 }),
      ]);
      if (current !== epoch.current) return;
      setEffective(data); setTemplates(list.items.filter(t => t.status === 'active')); setPermissions(list.permissions); setDevices(devs.items);
    } catch (e: any) { if (current === epoch.current) setError(e.message); }
  };
  useEffect(() => { setEditing(undefined); intent.current = undefined; void load(); return () => { epoch.current++; }; }, [station.id, deviceId]);
  const apply = async (scheme?: Scheme) => {
    if (busy || !effective) return;
    const t = templates.find(t => t.id === selected);
    if (!scheme && !t) return;
    const body = { station_id: station.id, device_id: deviceId || '', expected_version: effective.version, ...(scheme ? { scheme } : { template_id: t!.id, template_version: t!.version }) };
    const payload = JSON.stringify(body);
    if (intent.current?.payload !== payload) intent.current = { payload, request: crypto.randomUUID() };
    assertSession();setBusy(true);
    const current=epoch.current;try { await apiPost('/api/v1/admin/settings/charging-schemes/apply', { ...body, request_id: intent.current.request }); if(current!==epoch.current)return;setEditing(undefined); message.success('完整方案已应用，新订单使用此副本'); await load(); }
    catch (e: any) { setError(e.message); throw e; } finally { setBusy(false); }
  };
  return <Space direction="vertical" style={{ width: '100%' }}>
    <Space><Select aria-label="配置范围" style={{ width: 280 }} disabled={busy} value={deviceId || ''} onChange={v => onDeviceChange(v || null)} options={[{ value: '', label: `${station.name} · 站点生效方案` }, ...devices.map(d => ({ value: d.device_id, label: `设备 ${d.device_id}` }))]} /><Button disabled={busy} onClick={() => void load()}>刷新</Button></Space>
    {error && <Alert type="error" showIcon message={error} />}
    {deviceId && <DeviceCapabilities station={station.id} device={deviceId} editable={permissions.includes('device.metering')} />}
    {effective && <Card title={deviceId ? '设备生效方案' : '站点生效方案'} extra={<Tag>{effective.inherited ? '继承站点' : deviceId ? '设备独立配置' : '站点默认'}</Tag>}>
      {(permissions.includes('pricing.rule.create') || (deviceId && !effective.inherited && effective.scheme && permissions.includes('pricing.rule.update'))) && <Space wrap style={{ display: 'flex', marginBottom: 16 }}>
        {permissions.includes('pricing.rule.create') && <><Select aria-label="选择完整方案模板" style={{ width: 300 }} value={selected} onChange={setSelected} options={templates.map(t => ({ value: t.id, label: `${t.scheme.name} · v${t.version}` }))} placeholder="选择完整方案模板" /><Button type="primary" disabled={!selected || busy} loading={busy} onClick={() => void apply().catch(() => {})}>复制并应用整套方案</Button>{effective.scheme && <Button disabled={busy} onClick={() => setEditing(structuredClone(effective.scheme!))}>{deviceId ? '编辑设备独立方案' : '编辑站点方案'}</Button>}</>}
        {deviceId && !effective.inherited && effective.scheme && permissions.includes('pricing.rule.update') && <Popconfirm title="清除设备独立方案并恢复整套站点方案？" onConfirm={async () => { setBusy(true); try { assertSession();await apiPost(`/api/v1/admin/stations/${station.id}/charging-scheme/inherit`, { device_id: deviceId, expected_version: effective.version }); await load(); } catch (e: any) { setError(e.message); } finally { setBusy(false); } }}><Button disabled={busy}>恢复继承站点</Button></Popconfirm>}
      </Space>}
      {effective.scheme ? <SchemeDetails scheme={effective.scheme} /> : <Typography.Text>尚未应用完整充电方案</Typography.Text>}
    </Card>}
    <Drawer title={deviceId ? '设备独立方案' : '编辑站点生效方案'} width={1100} open={!!editing} onClose={() => { if (!busy) setEditing(undefined); }} closable={!busy} maskClosable={!busy} destroyOnHidden>{editing && <SchemeEditor value={editing} saving={busy} onSave={apply} />}</Drawer>
  </Space>;
}
