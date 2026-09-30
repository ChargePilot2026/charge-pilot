import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Col, Descriptions, Form, Input, InputNumber, Row, Select, Space, Spin, Tabs, Tag, Typography, message } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet, http, type ApiEnvelope } from '../../api/client';
import { LoadError } from '../../components/LoadError';
import DevicesPage from '../Devices';
import AppliedScheme from '../schemes/AppliedScheme';

export interface StationRecord {
  id: number; name: string; address?: string; longitude: number; latitude: number;
  status: string; contact_phone?: string;
}

export const stationStatuses: Record<string, { label: string; color: string }> = {
  active: { label: '运营中', color: 'green' },
  disabled: { label: '已停用', color: 'default' },
  construction: { label: '建设中', color: 'orange' },
};

export default function StationWorkspace({ station, permissions, revision = 0, initialDeviceId = null, onSaved }: {
  station: StationRecord;
  permissions: string[];
  revision?: number;
  initialDeviceId?: string | null;
  onSaved: (station: StationRecord) => void;
}) {
  const [detail, setDetail] = useState<StationRecord | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
  const [editing, setEditing] = useState(false);
  const [saving, setSaving] = useState(false);
  const [saveError, setSaveError] = useState('');
  const [form] = Form.useForm<Omit<StationRecord, 'id'>>();
  const [tab, setTab] = useState(permissions.includes('pricing.read') ? 'pricing' : 'devices');
  const [deviceID, setDeviceID] = useState<string | null>(initialDeviceId);
  const generation = useRef(0);

  const load = async () => {
    const current = ++generation.current;
    setLoading(true); setError(''); setDetail(null);
    try {
      const result = await apiGet<StationRecord>(`/api/v1/admin/stations/${station.id}`);
      if (current === generation.current) setDetail(result);
    } catch (cause: unknown) {
      if (current === generation.current) setError(cause instanceof Error ? cause.message : '站点详情读取失败');
    } finally {
      if (current === generation.current) setLoading(false);
    }
  };
  useEffect(() => { void load(); return () => { generation.current++; }; }, [station.id, revision, refresh]);
  useEffect(() => { setDeviceID(initialDeviceId); if (initialDeviceId) setTab('pricing'); }, [initialDeviceId]);

  if (loading) return <div style={{ padding: 48, textAlign: 'center' }}><Spin tip="加载站点管理"><div style={{ minHeight: 48 }} /></Spin></div>;
  if (error || !detail) return <LoadError title="站点管理加载失败" detail={error || '站点详情不存在'} onRetry={() => void load()} />;

  const edit = () => {
    form.setFieldsValue({ name: detail.name, status: detail.status, longitude: detail.longitude, latitude: detail.latitude,
      address: detail.address || '', contact_phone: detail.contact_phone || '' });
    setSaveError(''); setEditing(true);
  };
  const save = async () => {
    if (saving || !permissions.includes('station.update')) return;
    try {
      const values = await form.validateFields();
      setSaving(true); setSaveError('');
      const response = await http.put<ApiEnvelope<StationRecord>>(`/api/v1/admin/stations/${detail.id}`, values);
      const updated = response.data.data || { ...detail, ...values };
      setDetail(updated); setEditing(false); message.success('已保存'); onSaved(updated);
    } catch (cause: any) {
      if (!cause?.errorFields) setSaveError(cause instanceof Error ? cause.message : '保存失败');
    } finally { setSaving(false); }
  };

  const configureDevice = (id: string) => { setDeviceID(id); setTab('pricing'); };
  const tabs = [
    ...(permissions.includes('pricing.read') ? [{
      key: 'pricing', label: '充电方案',
      children: <AppliedScheme station={detail} deviceId={deviceID} onDeviceChange={setDeviceID} />,
    }] : []),
    ...(permissions.includes('device.read') ? [{
      key: 'devices', label: '设备',
      children: <DevicesPage station={detail} embedded onConfigure={configureDevice} />,
    }] : []),
  ];

  return <div className="station-workspace">
    <Card size="small" style={{ marginBottom: 16 }} title={<Space wrap>
      <Typography.Text strong copyable={{ text: String(detail.id) }}>{detail.name}（ID：{detail.id}）</Typography.Text>
      <Tag color={stationStatuses[detail.status]?.color}>{stationStatuses[detail.status]?.label || detail.status}</Tag>
    </Space>} extra={<Space wrap>
      <Button size="small" disabled={editing || saving} icon={<ReloadOutlined />} onClick={() => setRefresh(value => value + 1)}>刷新</Button>
      {permissions.includes('station.update') && (editing ? <>
        <Button size="small" disabled={saving} onClick={() => { setEditing(false); setSaveError(''); }}>取消</Button>
        <Button size="small" type="primary" loading={saving} onClick={() => void save()}>保存</Button>
      </> : <Button size="small" onClick={edit}>编辑基本信息</Button>)}
    </Space>}>
      {editing ? <Form form={form} layout="vertical" disabled={saving} onValuesChange={() => setSaveError('')}>
        <Row gutter={24}>
          <Col xs={24} sm={12} lg={8}><Form.Item name="name" label="名称" rules={[{ required: true, whitespace: true, message: '请输入站点名称' }]}><Input aria-label="站点名称" maxLength={128} /></Form.Item></Col>
          <Col xs={24} sm={12} lg={8}><Form.Item name="status" label="状态" rules={[{ required: true, message: '请选择站点状态' }]}><Select aria-label="站点运营状态" options={Object.entries(stationStatuses).map(([value, item]) => ({ value, label: item.label }))} /></Form.Item></Col>
          <Col xs={24} sm={12} lg={8}><Form.Item name="address" label="地址"><Input aria-label="站点地址" maxLength={255} /></Form.Item></Col>
          <Col xs={24} sm={12} lg={8}><Form.Item name="contact_phone" label="联系电话"><Input aria-label="联系电话" maxLength={32} /></Form.Item></Col>
          <Col xs={24} sm={12} lg={8}><Form.Item name="longitude" label="经度" rules={[{ required: true, message: '请输入经度' }]}><InputNumber aria-label="经度" min={-180} max={180} precision={8} style={{ width: '100%' }} /></Form.Item></Col>
          <Col xs={24} sm={12} lg={8}><Form.Item name="latitude" label="纬度" rules={[{ required: true, message: '请输入纬度' }]}><InputNumber aria-label="纬度" min={-90} max={90} precision={8} style={{ width: '100%' }} /></Form.Item></Col>
        </Row>
        {saveError && <Alert role="alert" type="error" showIcon message={saveError} />}
      </Form> : <Descriptions size="small" column={{ xs: 1, sm: 2 }} items={[
        { key: 'address', label: '地址', children: detail.address || '未填写' },
        { key: 'contact', label: '联系电话', children: detail.contact_phone || '未填写' },
        { key: 'coordinates', label: '经纬度', children: `${detail.longitude}, ${detail.latitude}` },
      ]} />}
    </Card>
    {detail.status !== 'active' && <Alert type="info" showIcon style={{ marginBottom: 16 }} message="当前站点尚未运营，可查看现有配置；启用后可新增设备和应用模板。" />}
    {tabs.length ? <Tabs activeKey={tabs.some(item => item.key === tab) ? tab : tabs[0].key} onChange={setTab} destroyOnHidden items={tabs} />
      : <Typography.Text type="secondary">当前账号可查看站点基本信息。</Typography.Text>}
  </div>;
}
