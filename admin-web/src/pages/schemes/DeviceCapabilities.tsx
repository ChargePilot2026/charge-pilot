import { useEffect, useRef, useState } from 'react';
import { Alert, Card, Descriptions, Space, Tag } from 'antd';
import { adminSession, apiGet } from '../../api/client';

type Cap = { max_minutes: number; duration: boolean; energy: boolean; online_card: boolean; stop_when_full: boolean; reports_energy: boolean; reports_segmented_power: boolean };
type Data = { protocol_adapter: string; capabilities: Cap };
const labels = { duration: '时长执行', energy: '电量执行', online_card: '在线刷卡', stop_when_full: '满充停止', reports_energy: '实际电量上报', reports_segmented_power: '功率上报' } as const;

export default function DeviceCapabilities({ station, device }: { station: number; device: string }) {
  const [data, setData] = useState<Data>();
  const [error, setError] = useState('');
  const epoch = useRef(0);
  useEffect(() => {
    const current = ++epoch.current, session = adminSession.epoch();
    setData(undefined); setError('');
    apiGet<Data>('/api/v1/admin/settings/device-capabilities', { station_id: station, device_id: device })
      .then(value => { if (current === epoch.current && session === adminSession.epoch()) setData(value); })
      .catch(cause => { if (current === epoch.current && session === adminSession.epoch()) setError(cause.message); });
    return () => { epoch.current++; };
  }, [station, device]);
  return <Card size="small" title="通信协议与设备能力" loading={!data && !error}>
    {error && <Alert type="error" message={error} />}
    {data && <Space direction="vertical" style={{ width: '100%' }}>
      <Descriptions size="small" items={[
        { key: 'protocol', label: '通信协议', children: data.protocol_adapter.toUpperCase() },
        { key: 'duration', label: '单次最长时长', children: `${data.capabilities.max_minutes / 60} 小时` },
      ]} />
      <Space wrap>{Object.entries(labels).map(([key, label]) => <Tag key={key} color={data.capabilities[key as keyof typeof labels] ? 'blue' : 'default'}>{label}：{data.capabilities[key as keyof typeof labels] ? '支持' : '不支持'}</Tag>)}</Space>
      <span style={{ color: '#8c8c8c' }}>能力由创建设备时选定的通信协议自动确定。满充停止是否启用按充电方案配置；在线卡加时由服务器完成，移开卡后再次刷卡触发新事件。</span>
    </Space>}
  </Card>;
}
