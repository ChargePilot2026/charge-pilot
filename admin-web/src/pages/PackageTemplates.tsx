import { useEffect, useState } from 'react';
import { Alert, Button, Form, Input, InputNumber, Modal, Select, Space, Switch, Table, Tag, message } from 'antd';
import { apiGet, apiPost, apiPut } from '../api/client';
import { fromCents, toCents, type Station } from './pricing/model';
import { LoadError } from '../components/LoadError';

// 金额方案按实际费率消费并封顶；固定时长套餐按配置的售价购买指定时长。
// 上架复制售价与时长，修改模板不改写在售记录或订单快照。

type PackageTemplate = {
  id: number; name: string; kind: 'amount' | 'package';
  price_cents: number; duration_minutes: number; min_charge_cents: number;
  show_remark: boolean; card_default: boolean; status: string; version: number;
  sort_order?: number;
  applied_targets?: string;
};
type FormValues = {
  name: string; kind: 'amount' | 'package'; price_yuan?: number; duration_minutes?: number;
  min_charge_yuan?: number; show_remark: boolean; card_default: boolean; sort_order: number; status: string;
};

const kindLabel = (kind: string) => (kind === 'amount' ? '金额方案' : '固定时长套餐');

export default function PackageTemplates() {
  const [items, setItems] = useState<PackageTemplate[]>([]);
  const [permissions, setPermissions] = useState<string[]>([]);
  const [loading, setLoading] = useState(false);
  const [saving, setSaving] = useState(false);

  const [editing, setEditing] = useState<PackageTemplate | null>(null);
  const [open, setOpen] = useState(false);
  const [formError, setFormError] = useState('');

  const [applying, setApplying] = useState<PackageTemplate | null>(null);
  const [stations, setStations] = useState<Station[]>([]);
  const [stationsLoading, setStationsLoading] = useState(false);
  const [targetStation, setTargetStation] = useState<number | null>(null);
  const [targetDevice, setTargetDevice] = useState('');
  const [applyError, setApplyError] = useState('');
  const [listError, setListError] = useState<string | null>(null);
  const [stationsError, setStationsError] = useState<string | null>(null);

  const [form] = Form.useForm();
  const kind = Form.useWatch<string>('kind', form) || 'amount';

  const load = async () => {
    setLoading(true);
    try {
      const result = await apiGet<{ items: PackageTemplate[]; permissions: string[] }>('/api/v1/admin/settings/package-templates');
      setItems(result.items || []);
      setPermissions(result.permissions || []);
      setListError(null);
    } catch (e: any) {
      setItems([]); setPermissions([]); setListError(e?.message || '套餐模板列表读取失败');
    } finally {
      setLoading(false);
    }
  };
  useEffect(() => { void load(); }, []);

  const canCreate = permissions.includes('pricing.rule.create');
  const canUpdate = permissions.includes('pricing.rule.update');

  const searchStations = async (keyword: string) => {
    setStationsLoading(true);
    try {
      const result = await apiGet<{ items: Station[] }>('/api/v1/admin/stations', { status: 'active', keyword: keyword || undefined, page: 1, page_size: 50 });
      setStations(result.items || []);
      setStationsError(null);
    } catch (e: any) {
      // 读不到站点时不能落到「没有匹配的运营中站点」那句空态上：那会把接口故障
      // 说成确实没有站点，运营于是以为套餐上不了架。
      setStations([]); setStationsError(e?.message || '运营中站点列表读取失败');
    } finally {
      setStationsLoading(false);
    }
  };

  const openEditor = (source?: PackageTemplate) => {
    setEditing(source || null);
    setFormError('');
    form.resetFields();
    form.setFieldsValue(source
      ? {
        name: source.name, kind: source.kind,
        price_yuan: fromCents(source.price_cents), duration_minutes: source.duration_minutes || undefined,
        min_charge_yuan: fromCents(source.min_charge_cents), show_remark: source.show_remark,
        card_default: source.card_default, sort_order: source.sort_order ?? 0, status: source.status,
      }
      : { name: '', kind: 'amount', price_yuan: 10, duration_minutes: 30, min_charge_yuan: 0, show_remark: true, card_default: false, sort_order: 0, status: 'active' });
    setOpen(true);
  };

  const save = async () => {
    let values: FormValues;
    try {
      values = await form.validateFields();
    } catch {
      return;
    }
    const isAmount = values.kind === 'amount';
    if (!(Number(values.price_yuan) > 0)) {
      setFormError(isAmount ? '请填写大于 0 的消费上限。' : '请填写大于 0 的套餐售价。');
      return;
    }
    if (!isAmount && (!Number.isInteger(values.duration_minutes) || Number(values.duration_minutes) < 1 || Number(values.duration_minutes) > 600)) {
      setFormError('请填写 1–600 分钟的整数充电时长。');
      return;
    }
    setSaving(true);
    setFormError('');
    try {
      const body = {
        name: values.name,
        kind: values.kind,
        price_cents: toCents(values.price_yuan),
        duration_minutes: isAmount ? 0 : Number(values.duration_minutes),
        min_charge_cents: isAmount ? toCents(values.min_charge_yuan) : 0,
        show_remark: !!values.show_remark,
        card_default: !!values.card_default,
        sort_order: Number(values.sort_order) || 0,
        status: values.status || 'active',
        expected_version: editing?.version || 0,
      };
      if (editing) await apiPut(`/api/v1/admin/settings/package-templates/${editing.id}`, body);
      else await apiPost('/api/v1/admin/settings/package-templates', body);
      message.success('套餐模板已保存');
      setOpen(false);
      await load();
    } catch (e: any) {
      setFormError(e.message || '保存失败');
    } finally {
      setSaving(false);
    }
  };

  const disable = (row: PackageTemplate) => Modal.confirm({
    title: '停用套餐模板',
    content: `停用「${row.name}」后不能再上架到新的站点或设备。已上架的套餐仍在售，需另行下架。`,
    okText: '停用',
    onOk: async () => {
      await apiPost(`/api/v1/admin/settings/package-templates/${row.id}/disable`);
      message.success('已停用');
      await load();
    },
  });

  const openApply = async (row: PackageTemplate) => {
    setApplying(row);
    setApplyError('');
    setTargetStation(null);
    setTargetDevice('');
    setStations([]);
    await searchStations('');
  };

  const apply = async () => {
    if (!applying) return;
    if (!targetStation) {
      setApplyError('请选择要上架到的站点');
      return;
    }
    setSaving(true);
    setApplyError('');
    try {
      await apiPost(`/api/v1/admin/settings/package-templates/${applying.id}/apply`, {
        station_id: targetStation,
        device_id: targetDevice.trim(),
      });
      message.success('套餐已上架');
      setApplying(null);
      await load();
    } catch (e: any) {
      setApplyError(e.message || '上架失败');
    } finally {
      setSaving(false);
    }
  };

  return <>
    <Space style={{ marginBottom: 12 }}>
      <Button onClick={() => void load()} loading={loading}>刷新</Button>
      {canCreate && <Button type="primary" onClick={() => openEditor()}>新建套餐模板</Button>}
    </Space>
    <Alert type="info" showIcon style={{ marginBottom: 12 }}
      message="金额方案按现行费率消费，达到上限后停止；固定时长套餐按配置售价支付，到时停止，提前结束按未使用时长退款。应用会复制成一条在售记录，之后修改模板不会影响已上架的套餐。" />
    {listError && <LoadError title="套餐模板列表加载失败" detail={listError} onRetry={() => void load()} />}
    <Table rowKey="id" dataSource={items} loading={loading} scroll={{ x: 1000 }} pagination={false} columns={[
      { title: '名称', render: (_: unknown, r: PackageTemplate) => <>{r.name}<div style={{ color: '#999' }}>v{r.version}</div></> },
      { title: '排序', dataIndex: 'sort_order', width: 90, render: (v?: number) => (typeof v === 'number' ? v : <span style={{ color: '#999' }}>—</span>) },
      { title: '类型', dataIndex: 'kind', render: (k: string) => <Tag color={k === 'amount' ? 'blue' : 'green'}>{kindLabel(k)}</Tag> },
      { title: '消费上限 / 套餐售价', render: (_: unknown, r: PackageTemplate) => (r.price_cents ? `¥${fromCents(r.price_cents).toFixed(2)}` : '—') },
      { title: '时长', render: (_: unknown, r: PackageTemplate) => (r.duration_minutes ? `${r.duration_minutes} 分钟` : '—') },
      { title: '最低消费', render: (_: unknown, r: PackageTemplate) => (r.min_charge_cents ? `¥${fromCents(r.min_charge_cents).toFixed(2)}` : '—') },
      { title: '标记', render: (_: unknown, r: PackageTemplate) => <Space size={4}>
        {r.show_remark && <Tag>显示说明</Tag>}
        {r.card_default && <Tag color="gold">刷卡默认</Tag>}
      </Space> },
      { title: '状态', render: (_: unknown, r: PackageTemplate) => <Tag color={r.status !== 'active' ? 'default' : r.price_cents <= 0 ? 'orange' : 'green'}>{r.status !== 'active' ? '已停用' : r.price_cents <= 0 ? '待补充售价' : '可上架'}</Tag> },
      { title: '已上架', dataIndex: 'applied_targets', render: (v?: string) => v || <span style={{ color: '#999' }}>未上架</span> },
      {
        title: '操作', render: (_: unknown, r: PackageTemplate) => <Space>
          {canCreate && r.status === 'active' && <Button type="link" disabled={r.price_cents <= 0} onClick={() => void openApply(r)}>上架</Button>}
          {canUpdate && <Button type="link" onClick={() => openEditor(r)}>编辑</Button>}
          {canUpdate && r.status === 'active' && <Button type="link" danger onClick={() => disable(r)}>停用</Button>}
        </Space>,
      },
    ]} />

    <Modal title={editing ? `编辑套餐模板 · ${editing.name}` : '新建套餐模板'} open={open} width={720}
      onCancel={() => setOpen(false)} onOk={() => void save()} confirmLoading={saving} okText="保存" destroyOnHidden>
      {formError && <Alert type="error" showIcon message={formError} style={{ marginBottom: 12 }} />}
      <Form form={form} name="package_template" layout="vertical">
        <Form.Item name="name" label="套餐名称" rules={[{ required: true, whitespace: true, max: 64 }]}>
          <Input maxLength={64} placeholder={kind === 'amount' ? '如：10 元消费上限' : '如：2 小时 / 5 元'} style={{ width: 280 }} />
        </Form.Item>
        <Form.Item name="kind" label="套餐类型" rules={[{ required: true }]}
          extra="金额方案填写消费上限；固定时长套餐同时填写售价和时长，用户按售价付款。">
          <Select style={{ width: 280 }} options={[
            { value: 'amount', label: '金额方案（按实际费率消费）' },
            { value: 'package', label: '固定时长套餐（售价＋时长）' },
          ]} />
        </Form.Item>
        <Form.Item name="price_yuan" label={kind === 'amount' ? '消费上限（元）' : '套餐售价（元）'}
          rules={[{ required: true, message: kind === 'amount' ? '请填写消费上限' : '请填写套餐售价' }]}
          extra={kind === 'amount' ? '用户预付此金额，按实际费率消费，剩余金额按结算结果退款。' : '用户选择本套餐时支付此价格，不再按站点费率重复收费。'}>
          <InputNumber min={0.01} max={10000} step={0.5} precision={2} addonBefore="¥" style={{ width: 200 }} />
        </Form.Item>
        {kind === 'package' && <>
          {editing && editing.price_cents <= 0 && <Alert type="warning" showIcon style={{ marginBottom: 12 }} message="此旧套餐缺少售价，请补充后保存。已上架的旧记录需下架后重新上架。" />}
          <Form.Item name="duration_minutes" label="充电时长（分钟）" rules={[{ required: true, message: '请填写充电时长' }]}
            extra="1–600 分钟，到时停止；提前结束按未使用时长退款。例如：120 分钟售价 5 元，充满收 5 元，用 60 分钟收 2.50 元。">
            <InputNumber min={1} max={600} step={30} precision={0} addonAfter="分钟" style={{ width: 200 }} />
          </Form.Item>
        </>}
        <Space align="start" wrap>
          {kind === 'amount' && <Form.Item name="min_charge_yuan" label="最低消费（元）" extra="低于该金额按该金额收，0 表示不设门槛。">
            <InputNumber min={0} max={10000} step={0.5} precision={2} addonBefore="¥" style={{ width: 180 }} />
          </Form.Item>}
          <Form.Item name="sort_order" label="排序" extra="越小越靠前。">
            <InputNumber min={0} max={10000} style={{ width: 140 }} />
          </Form.Item>
        </Space>
        <Space size="large" wrap>
          <Form.Item name="show_remark" label="小程序显示套餐说明" valuePropName="checked"><Switch /></Form.Item>
          <Form.Item name="card_default" label="作为刷卡默认套餐" valuePropName="checked"><Switch /></Form.Item>
          <Form.Item name="status" label="状态" rules={[{ required: true }]}>
            <Select style={{ width: 140 }} options={[{ value: 'active', label: '可上架' }, { value: 'disabled', label: '已停用' }]} />
          </Form.Item>
        </Space>
      </Form>
    </Modal>

    <Modal title={applying ? `上架「${applying.name}」` : '上架套餐'} open={!!applying}
      onCancel={() => setApplying(null)} onOk={() => void apply()} confirmLoading={saving} okText="上架"
      okButtonProps={{ disabled: !targetStation }}>
      {applyError && <Alert type="error" showIcon message={applyError} style={{ marginBottom: 12 }} />}
      {stationsError && <LoadError title="运营中站点列表加载失败" detail={stationsError} onRetry={() => void searchStations('')} />}
      <Alert type="warning" showIcon style={{ marginBottom: 12 }}
        message="上架会把当前模板复制成一条在售记录。同一个套餐不能在同一个范围重复上架；站点与具体设备可以各上架一份。" />
      <Space direction="vertical" style={{ width: '100%' }}>
        <Select showSearch allowClear aria-label="选择站点" placeholder="输入站点名称或地址进行筛选"
          value={targetStation ?? undefined} loading={stationsLoading} filterOption={false}
          onSearch={value => void searchStations(value)} onChange={value => setTargetStation(value ?? null)}
          options={stations.map(s => ({ value: s.id, label: s.name }))} style={{ width: '100%' }} />
        <Input aria-label="设备编号" placeholder="设备编号（留空表示上架为站点默认套餐）" maxLength={64}
          value={targetDevice} onChange={e => setTargetDevice(e.target.value)} />
      </Space>
      {stations.length === 0 && !stationsLoading && !stationsError && <div style={{ marginTop: 8, color: '#999' }}>没有匹配的运营中站点</div>}
    </Modal>
  </>;
}
