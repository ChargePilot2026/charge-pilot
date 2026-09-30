import { useEffect, useRef, useState } from 'react';
import { Alert, Button, Card, Descriptions, Space, Spin, Tabs, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet } from '../../api/client';
import { LoadError } from '../../components/LoadError';
import DevicesPage from '../Devices';
import AppliedScheme from '../schemes/AppliedScheme';

export interface StationRecord {
  id: number; name: string; address?: string; longitude: number; latitude: number;
  status: string; open_hours?: string; contact_phone?: string;
}

export const stationStatuses: Record<string, { label: string; color: string }> = {
  active: { label: '运营中', color: 'green' },
  disabled: { label: '已停用', color: 'default' },
  construction: { label: '建设中', color: 'orange' },
};

export default function StationWorkspace({ station, permissions, revision = 0, initialDeviceId = null, onEdit }: {
  station: StationRecord;
  permissions: string[];
  revision?: number;
  initialDeviceId?: string | null;
  onEdit: (station: StationRecord) => void;
}) {
  const [detail, setDetail] = useState<StationRecord | null>(null);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState('');
  const [refresh, setRefresh] = useState(0);
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

  if (loading) return <div style={{ padding: 48, textAlign: 'center' }}><Spin tip="加载站点工作区"><div style={{ minHeight: 48 }} /></Spin></div>;
  if (error || !detail) return <LoadError title="站点工作区加载失败" detail={error || '站点详情不存在'} onRetry={() => void load()} />;

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
      <Typography.Text strong>{detail.name}</Typography.Text>
      <Tag color={stationStatuses[detail.status]?.color}>{stationStatuses[detail.status]?.label || detail.status}</Tag>
    </Space>} extra={<Space wrap>
      <Button size="small" icon={<ReloadOutlined />} onClick={() => setRefresh(value => value + 1)}>刷新工作区</Button>
      {permissions.includes('station.update') && <Button size="small" onClick={() => onEdit(detail)}>编辑基本信息</Button>}
    </Space>}>
      <Descriptions size="small" column={{ xs: 1, sm: 2 }} items={[
        { key: 'id', label: '站点 ID', children: <Typography.Text copyable={{ text: String(detail.id) }}>{detail.id}</Typography.Text> },
        { key: 'hours', label: '营业时间', children: detail.open_hours || '未填写' },
        { key: 'address', label: '地址', children: detail.address || '未填写' },
        { key: 'contact', label: '联系电话', children: detail.contact_phone || '未填写' },
        { key: 'coordinates', label: '经纬度', children: `${detail.longitude}, ${detail.latitude}` },
      ]} />
    </Card>
    {detail.status !== 'active' && <Alert type="info" showIcon style={{ marginBottom: 16 }} message="当前站点尚未运营，可查看现有配置；启用后可新增设备和应用模板。" />}
    {tabs.length ? <Tabs activeKey={tabs.some(item => item.key === tab) ? tab : tabs[0].key} onChange={setTab} destroyOnHidden items={tabs} />
      : <Typography.Text type="secondary">当前账号可查看站点基本信息。</Typography.Text>}
  </div>;
}
