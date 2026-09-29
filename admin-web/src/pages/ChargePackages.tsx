import { useEffect, useState } from 'react';
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Space, Table, Tag, message } from 'antd';
import { apiGet, apiPost, apiPut } from '../api/client';

// A charge package is a template. It is not sellable until it is applied to a
// station, and applying copies it into a station-scoped offer that the mini
// program reads. The copy is deliberate: editing the package afterwards must not
// change what a station already sells.
type ChargePackage = {
  id: number;
  code: string;
  name: string;
  mode: 'amount' | 'package';
  price_cents: number;
  duration_minutes: number;
  status: 'active' | 'disabled';
  version: number;
  applied_stations?: string;
};

type Station = { id: number; code: string; name: string; address?: string; status: string };

const yuan = (cents: number) => `¥${(cents / 100).toFixed(2)}`;

export default function ChargePackages() {
  const [items, setItems] = useState<ChargePackage[]>([]);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);
  const [editing, setEditing] = useState<ChargePackage | null>(null);
  const [open, setOpen] = useState(false);
  const [error, setError] = useState('');

  // The apply dialog keeps its own target and station list so opening it does
  // not disturb the edit form behind it.
  const [applying, setApplying] = useState<ChargePackage | null>(null);
  const [stations, setStations] = useState<Station[]>([]);
  const [stationsLoading, setStationsLoading] = useState(false);
  const [stationKeyword, setStationKeyword] = useState('');
  const [applyError, setApplyError] = useState('');
  const [selectedStation, setSelectedStation] = useState<number | null>(null);

  const [form] = Form.useForm();
  const mode = Form.useWatch('mode', form);

  const load = async () => {
    setLoading(true);
    try {
      const result = await apiGet<{ items: ChargePackage[]; permissions: string[] }>('/api/v1/admin/settings/charge-packages');
      setItems(result.items);
      setPermissions(result.permissions || []);
    } catch (e: any) {
      message.error(e.message);
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, []);

  const canCreate = permissions.includes('pricing.rule.create');
  const canUpdate = permissions.includes('pricing.rule.update');

  // Only active stations can be applied to, because a disabled station must not
  // gain a sellable package.
  const searchStations = async (keyword: string) => {
    setStationsLoading(true);
    try {
      const result = await apiGet<{ items: Station[] }>('/api/v1/admin/stations', { status: 'active', keyword: keyword || undefined, page: 1, page_size: 50 });
      setStations(result.items || []);
    } catch (e: any) {
      message.error(e.message);
    } finally {
      setStationsLoading(false);
    }
  };

  const openApply = async (pkg: ChargePackage) => {
    setApplying(pkg);
    setApplyError('');
    setStationKeyword('');
    setStations([]);
    await searchStations('');
  };

  const apply = async () => {
    if (!applying) return;
    if (!selectedStation) {
      setApplyError('请选择要应用到的站点');
      return;
    }
    setSaving(true);
    setApplyError('');
    try {
      await apiPost(`/api/v1/admin/settings/charge-packages/${applying.id}/apply`, { station_id: selectedStation });
      message.success('套餐已应用到所选站点');
      setApplying(null);
      await load();
    } catch (e: any) {
      setApplyError(e.message || '应用失败');
    } finally {
      setSaving(false);
    }
  };

  const edit = (pkg?: ChargePackage) => {
    setEditing(pkg || null);
    setError('');
    form.resetFields();
    // The form works in yuan because that is what an operator reads off a price
    // list; the API keeps integer cents.
    form.setFieldsValue(pkg ? { ...pkg, price_yuan: pkg.price_cents / 100 } : { mode: 'amount', status: 'active', duration_minutes: 0 });
    setOpen(true);
  };

  const save = async () => {
    try {
      const values = await form.validateFields();
      setSaving(true);
      setError('');
      const body = {
        code: values.code,
        name: values.name,
        mode: values.mode,
        price_cents: Math.round(Number(values.price_yuan) * 100),
        duration_minutes: values.mode === 'amount' ? 0 : values.duration_minutes,
        status: values.status,
        expected_version: editing?.version || 0,
      };
      if (editing) await apiPut(`/api/v1/admin/settings/charge-packages/${editing.id}`, body);
      else await apiPost('/api/v1/admin/settings/charge-packages', body);
      message.success('充电套餐已保存');
      setOpen(false);
      await load();
    } catch (e: any) {
      if (!e.errorFields) setError(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  return <>
    <Space style={{ marginBottom: 12 }}>
      <Button onClick={() => void load()} loading={loading}>刷新套餐</Button>
      {canCreate && <Button type="primary" onClick={() => edit()}>新增套餐</Button>}
    </Space>
    <Alert type="info" showIcon message="套餐是模板，需要“应用到站点”后才能在用户端购买。金额充电按实际费用消耗固定金额；时长套餐按已使用秒数结算，提前结束的未使用时长原路退款。修改套餐不会改变已应用站点的价格，也不改变已付款订单的快照。" style={{ marginBottom: 12 }} />
    <Table rowKey="id" dataSource={items} loading={loading} columns={[
      { title: '套餐', render: (_: unknown, row: ChargePackage) => <>{row.name}<div>{row.code}</div></> },
      { title: '方式', dataIndex: 'mode', render: (value: string) => value === 'amount' ? '金额充电' : '时长套餐' },
      { title: '价格', dataIndex: 'price_cents', render: (value: number) => yuan(value) },
      { title: '时长', dataIndex: 'duration_minutes', render: (value: number) => value ? `${value} 分钟` : '按金额上限' },
      { title: '状态', dataIndex: 'status', render: (value: string) => <Tag color={value === 'active' ? 'green' : 'default'}>{value === 'active' ? '可售' : '已停用'}</Tag> },
      { title: '已应用站点', dataIndex: 'applied_stations', render: (value?: string) => value || <span style={{ color: '#999' }}>未应用</span> },
      {
        title: '操作',
        render: (_: unknown, row: ChargePackage) => <Space>
          {canCreate && <Button type="link" onClick={() => void openApply(row)}>应用到站点</Button>}
          {canUpdate && <Button type="link" onClick={() => edit(row)}>编辑</Button>}
        </Space>,
      },
    ]} />

    <Modal title={editing ? '编辑充电套餐' : '新增充电套餐'} open={open} onCancel={() => setOpen(false)} onOk={() => void save()} confirmLoading={saving} okText="保存">
      {error && <Alert type="error" showIcon message={error} style={{ marginBottom: 12 }} />}
      <Form form={form} layout="vertical">
        <Form.Item name="code" label="套餐编码" rules={[{ required: true, whitespace: true, max: 64 }]}><Input maxLength={64} /></Form.Item>
        <Form.Item name="name" label="套餐名称" rules={[{ required: true, whitespace: true, max: 128 }]}><Input maxLength={128} /></Form.Item>
        <Form.Item name="mode" label="充电方式" rules={[{ required: true }]}>
          <Select options={[{ value: 'amount', label: '金额充电' }, { value: 'package', label: '时长套餐' }]} />
        </Form.Item>
        <Form.Item name="price_yuan" label="价格（元）" rules={[{ required: true }]}>
          <InputNumber min={0.01} max={10000} step={0.5} precision={2} addonBefore="¥" style={{ width: '100%' }} />
        </Form.Item>
        {mode === 'package' && <Form.Item name="duration_minutes" label="套餐时长（分钟）" rules={[{ required: true }]}><InputNumber min={1} max={600} precision={0} style={{ width: '100%' }} /></Form.Item>}
        <Form.Item name="status" label="状态" rules={[{ required: true }]}>
          <Select options={[{ value: 'active', label: '可售' }, { value: 'disabled', label: '停用' }]} />
        </Form.Item>
      </Form>
    </Modal>

    <Modal
      title={applying ? `将「${applying.name}」应用到站点` : '应用到站点'}
      open={!!applying}
      onCancel={() => setApplying(null)}
      onOk={() => void apply()}
      confirmLoading={saving}
      okText="保存"
      okButtonProps={{ disabled: !selectedStation }}
    >
      {applyError && <Alert type="error" showIcon message={applyError} style={{ marginBottom: 12 }} />}
      <Alert type="warning" showIcon message="此操作会为所选站点新增一个可售套餐，同一套餐不能重复应用到同一站点。已付款订单的价格不受影响。" style={{ marginBottom: 12 }} />
      <Select
        showSearch
        allowClear
        aria-label="选择站点"
        placeholder="输入站点编码、名称或地址进行筛选"
        value={selectedStation ?? undefined}
        loading={stationsLoading}
        filterOption={false}
        onSearch={value => { setStationKeyword(value); void searchStations(value); }}
        onChange={value => setSelectedStation(value ?? null)}
        options={stations.map(station => ({
          value: station.id,
          label: `${station.name}（${station.code}）`,
        }))}
        style={{ width: '100%' }}
      />
      {stations.length === 0 && !stationsLoading && <div style={{ marginTop: 8, color: '#999' }}>没有匹配的运营中站点</div>}
    </Modal>
  </>;
}
