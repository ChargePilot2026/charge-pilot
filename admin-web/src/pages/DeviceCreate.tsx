import { Alert, Button, Collapse, Form, Input, InputNumber, Modal, Select, Space, Spin, Switch, message } from 'antd';
import { PlusOutlined } from '@ant-design/icons';
import { useEffect, useRef, useState } from 'react';
import { apiGet, apiPost } from '../api/client';
import { LoadError } from '../components/LoadError';
import { MODE_OPTIONS, type ChargeMode } from './pricing/model';

type StationChoice = { id: number; name: string };
type DeviceForm = {
  device_id: string;
  vendor_id: number;
  station_id: number;
  port_count: number;
  model?: string;
  charge_mode: ChargeMode;
  reports_energy: boolean;
  reports_segmented_power: boolean;
};
type CreateJob = { import_id: string; status: string; last_error?: string | null };

// 手动新建复用导入的开通流程，只有网关设备、端口与后台元数据均确认后才算成功。
export default function DeviceCreate({ onComplete, canReadStations }: {
  onComplete: () => void;
  canReadStations: boolean;
}) {
  const [form] = Form.useForm<DeviceForm>();
  const [open, setOpen] = useState(false);
  const [saving, setSaving] = useState(false);
  const [error, setError] = useState('');
  const [stations, setStations] = useState<StationChoice[]>([]);
  const [stationsLoading, setStationsLoading] = useState(false);
  const [stationsError, setStationsError] = useState('');
  const stationGeneration = useRef(0);
  const stationKeyword = useRef('');
  const stationSearchTimer = useRef<ReturnType<typeof setTimeout>>();
  const submitting = useRef(false);
  const request = useRef<{ signature: string; id: string }>();

  useEffect(() => () => {
    stationGeneration.current++;
    clearTimeout(stationSearchTimer.current);
  }, []);

  const searchStations = async (keyword: string) => {
    clearTimeout(stationSearchTimer.current);
    const current = ++stationGeneration.current;
    stationKeyword.current = keyword;
    setStationsLoading(true);
    setStationsError('');
    try {
      const result = await apiGet<{ items: StationChoice[] }>('/api/v1/admin/stations', {
        status: 'active', keyword: keyword || undefined, page: 1, page_size: 50,
      });
      if (current !== stationGeneration.current) return;
      const selected = form.getFieldValue('station_id');
      setStations(previous => {
        const selectedChoice = previous.find(station => station.id === selected);
        const items = result.items || [];
        return selectedChoice && !items.some(station => station.id === selected)
          ? [selectedChoice, ...items] : items;
      });
    } catch (cause: unknown) {
      if (current === stationGeneration.current) {
        setStationsError(cause instanceof Error ? cause.message : '站点读取失败');
      }
    } finally {
      if (current === stationGeneration.current) setStationsLoading(false);
    }
  };

  const showEditor = () => {
    form.resetFields();
    form.setFieldsValue({ port_count: 2, charge_mode: 'device_duration', reports_energy: false, reports_segmented_power: false });
    request.current = undefined;
    setError('');
    setStationsError('');
    setStations([]);
    setOpen(true);
    if (canReadStations) void searchStations('');
  };

  const create = async () => {
    if (submitting.current) return;
    submitting.current = true;
    try {
      const values = await form.validateFields();
      const device = {
        device_id: values.device_id.trim(), vendor_id: values.vendor_id,
        station_id: values.station_id, port_count: values.port_count,
        model: values.model?.trim() || null, charge_mode: values.charge_mode,
        reports_energy: !!values.reports_energy, reports_segmented_power: !!values.reports_segmented_power,
      };
      const signature = JSON.stringify(device);
      // 超时或失败后，未修改配置的重试沿用同一个幂等键。
      if (request.current?.signature !== signature) request.current = { signature, id: crypto.randomUUID() };
      setSaving(true);
      setError('');
      const result = await apiPost<CreateJob>('/api/v1/admin/device-imports', {
        import_id: request.current.id, devices: [device],
      });
      if (result.status !== 'completed') {
        setError(result.last_error || '设备尚未完成创建，请保留当前信息后重试。');
        return;
      }
      message.success('设备已创建');
      setOpen(false);
      form.resetFields();
      request.current = undefined;
      onComplete();
    } catch (cause: unknown) {
      if (!(cause && typeof cause === 'object' && 'errorFields' in cause)) {
        setError(cause instanceof Error ? cause.message : '设备创建失败，请稍后重试');
      }
    } finally {
      submitting.current = false;
      setSaving(false);
    }
  };

  const positiveID = { validator: (_: unknown, value: number | undefined) =>
    value === undefined || (Number.isSafeInteger(value) && value > 0)
      ? Promise.resolve() : Promise.reject(new Error('请输入有效的正整数 ID')) };

  return <>
    <Button type="primary" icon={<PlusOutlined />} onClick={showEditor}>新建</Button>
    <Modal title="新建设备" open={open} width={560} confirmLoading={saving}
      onOk={() => void create()} okText="创建" cancelText="取消"
      onCancel={() => { if (!saving) setOpen(false); }} closable={!saving} maskClosable={!saving} keyboard={!saving}>
      {error && <Alert type="error" showIcon message="设备创建失败" description={error} style={{ marginBottom: 16 }} />}
      <Form form={form} name="device_create" layout="vertical" disabled={saving} onFinish={() => void create()}>
        <Form.Item name="device_id" label="设备编号" normalize={(value: string) => value.trim()}
          extra="8–32 位字母、数字、下划线或短横线，大小写不区分重复。"
          rules={[{ required: true, message: '请填写设备编号' }, { pattern: /^[A-Za-z0-9_-]{8,32}$/, message: '设备编号须为 8–32 位字母、数字、下划线或短横线' }]}>
          <Input maxLength={32} placeholder="如：CP000001" />
        </Form.Item>
        <Form.Item name="vendor_id" label="厂商 ID" extra="填写已启用厂商的 ID。"
          rules={[{ required: true, message: '请填写厂商 ID' }, positiveID]}>
          <InputNumber min={1} max={Number.MAX_SAFE_INTEGER} precision={0} placeholder="厂商 ID" style={{ width: '100%' }} />
        </Form.Item>
        <Form.Item name="station_id" label="所属站点" extra={canReadStations ? '仅可选择运营中的站点。' : '填写运营中站点的 ID。'}
          rules={[{ required: true, message: '请选择或填写所属站点' }, positiveID]}>
          {canReadStations ? <Select showSearch allowClear filterOption={false} loading={stationsLoading}
            placeholder="搜索站点名称或地址" options={stations.map(station => ({ value: station.id, label: `${station.name} · ID ${station.id}` }))}
            onSearch={keyword => {
              clearTimeout(stationSearchTimer.current);
              // 等待防抖期间就作废先前请求，避免旧结果覆盖新搜索。
              stationGeneration.current++;
              stationSearchTimer.current = setTimeout(() => { void searchStations(keyword); }, 250);
            }}
            notFoundContent={stationsLoading ? <Spin size="small" /> : stationsError ? '站点加载失败，请重试' : '没有匹配的运营中站点'} />
            : <InputNumber min={1} max={Number.MAX_SAFE_INTEGER} precision={0} placeholder="站点 ID" style={{ width: '100%' }} />}
        </Form.Item>
        {stationsError && <LoadError title="站点选项加载失败" detail={stationsError} onRetry={() => void searchStations(stationKeyword.current)} />}
        <Form.Item name="port_count" label="充电端口数" extra="1–255 个；dc589 设备最多 20 个。"
          rules={[{ required: true, message: '请填写端口数' }, { type: 'integer', min: 1, max: 255, message: '端口数须为 1–255 的整数' }]}>
          <InputNumber min={1} max={255} precision={0} style={{ width: '100%' }} />
        </Form.Item>
        <Form.Item name="model" label="型号（选填）" rules={[{ max: 128, message: '型号最多 128 个字符' }]}>
          <Input maxLength={128} placeholder="设备型号" />
        </Form.Item>
        <Collapse ghost items={[{ key: 'capabilities', label: '计费方式与计量能力', forceRender: true, children: <>
          <Form.Item name="charge_mode" label="初始计费方式" extra="站点已配置计费规则时，按站点规则执行。">
            <Select options={MODE_OPTIONS as never} />
          </Form.Item>
          <Space direction="vertical" style={{ width: '100%' }}>
            <Form.Item name="reports_energy" label="设备支持上报电量" valuePropName="checked"><Switch /></Form.Item>
            <Form.Item name="reports_segmented_power" label="设备支持上报分段功率" valuePropName="checked"><Switch /></Form.Item>
          </Space>
          <div style={{ color: '#8c8c8c' }}>请按设备实际能力填写。站点按电量或功率计费时，需要对应的计量能力。</div>
        </> }]} />
      </Form>
    </Modal>
  </>;
}
