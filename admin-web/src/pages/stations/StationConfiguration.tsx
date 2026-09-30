import { useCallback, useEffect, useRef, useState, type MutableRefObject } from 'react';
import { Alert, App, Button, Card, Descriptions, Empty, Modal, Select, Space, Spin, Table, Tag, Typography } from 'antd';
import { apiGet, apiPost } from '../../api/client';
import { LoadError } from '../../components/LoadError';
import { describeSpec, fromCents, isServerBilled, modeLabel, type Spec } from '../pricing/model';

type Station = { id: number; name: string; status: string };
type Rule = { id: number; name: string; template_id?: number; spec_json?: Spec; version: number };
type Offer = {
  id: number; station_id: number; device_id?: string | null; package_template_id?: number;
  name: string; mode: string; price_cents: number; duration_minutes: number; min_charge_cents: number;
  show_remark: boolean; card_default: boolean; status: string; version: number;
};
type Configuration = {
  station_id: number; default_rule: Rule | null; station_latest_version: number;
  offers: Offer[]; permissions: string[]; can_manage_default: boolean;
};
type DevicePricing = {
  device_id: string; model?: string; template_name?: string; template_id?: number; spec_json?: Spec;
  own_rule_id?: number; own_version?: number; own_latest_version?: number; station_version?: number;
  effective_rule_device_id?: string | null; offer_count?: number;
};
type Matrix = { station_id: number; items: DevicePricing[] };
type Candidate = {
  id: number; name: string; version: number; status: string; spec?: Spec; unavailable_reason?: string;
};
type PackageTemplate = {
  id: number; name: string; kind: string; price_cents: number; duration_minutes: number;
  min_charge_cents: number; show_remark: boolean; card_default: boolean; status: string;
};
type Publication = { version?: number; switch_pending?: boolean; switch_task_id?: number; replayed?: boolean };
type Intent = { key: string; requestId: string };

export interface StationConfigurationProps {
  station: Station;
  deviceId?: string | null;
  onDeviceChange?: (id: string | null) => void;
  onChanged?: () => void;
}

const isStationOffer = (offer: Offer) => !offer.device_id;
const offerKind = (kind: string) => kind === 'amount' ? '按金额封顶' : kind === 'package' ? '按时长套餐' : kind;
const money = (cents: number) => `¥${fromCents(cents).toFixed(2)}`;
const errorText = (error: unknown, fallback: string) => error instanceof Error ? error.message : fallback;

function offerTerms(offer: Offer | PackageTemplate) {
  const kind = 'mode' in offer ? offer.mode : offer.kind;
  return <Descriptions size="small" column={{ xs: 1, sm: 2 }} bordered items={[
    { key: 'name', label: '套餐', children: offer.name },
    { key: 'kind', label: '类型', children: offerKind(kind) },
    { key: 'value', label: kind === 'package' ? '时长' : '封顶金额', children: kind === 'package' ? `${offer.duration_minutes} 分钟` : money(offer.price_cents) },
    { key: 'minimum', label: '最低消费', children: money(offer.min_charge_cents) },
    { key: 'card', label: '刷卡默认', children: offer.card_default ? '是' : '否' },
    { key: 'remark', label: '显示套餐说明', children: offer.show_remark ? '是' : '否' },
  ]} />;
}

function requestIdFor(intent: MutableRefObject<Intent | null>, key: string) {
  if (intent.current?.key !== key) intent.current = { key, requestId: crypto.randomUUID() };
  return intent.current.requestId;
}

export default function StationConfiguration({ station, deviceId, onDeviceChange, onChanged }: StationConfigurationProps) {
  const { message, modal } = App.useApp();
  const [localDevice, setLocalDevice] = useState<string | null>(null);
  const targetDevice = deviceId === undefined ? localDevice : deviceId;
  const targetKey = `${station.id}:${targetDevice || ''}`;
  const context = useRef(targetKey);
  context.current = targetKey;
  const generation = useRef(0);
  const modalGeneration = useRef(0);
  const confirmations = useRef<ReturnType<typeof Modal.confirm>[]>([]);
  const [loaded, setLoaded] = useState<{ configuration: Configuration; matrix: Matrix } | null>(null);
  const [loading, setLoading] = useState(false);
  const [loadError, setLoadError] = useState('');
  const [saving, setSaving] = useState<'pricing' | 'package' | 'disable' | 'reset' | null>(null);

  const [pricingOpen, setPricingOpen] = useState(false);
  const [candidates, setCandidates] = useState<Candidate[]>([]);
  const [candidateLoading, setCandidateLoading] = useState(false);
  const [candidateError, setCandidateError] = useState('');
  const [selectedTemplate, setSelectedTemplate] = useState<number | null>(null);
  const [pricingError, setPricingError] = useState('');
  const pricingIntent = useRef<Intent | null>(null);

  const [packageOpen, setPackageOpen] = useState(false);
  const [packages, setPackages] = useState<PackageTemplate[]>([]);
  const [packageLoading, setPackageLoading] = useState(false);
  const [packageLoadError, setPackageLoadError] = useState('');
  const [selectedPackage, setSelectedPackage] = useState<number | null>(null);
  const [packageError, setPackageError] = useState('');
  const packageIntent = useRef<Intent | null>(null);

  const load = useCallback(async () => {
    const current = ++generation.current;
    setLoading(true); setLoadError(''); setLoaded(null);
    try {
      const [configuration, matrix] = await Promise.all([
        apiGet<Configuration>(`/api/v1/admin/stations/${station.id}/configuration`),
        apiGet<Matrix>('/api/v1/admin/settings/device-pricing', { station_id: station.id }),
      ]);
      if (current !== generation.current) return;
      if (configuration?.station_id !== station.id || !Array.isArray(configuration.offers) || !Array.isArray(configuration.permissions)
        || !configuration.permissions.every(permission => typeof permission === 'string') || typeof configuration.can_manage_default !== 'boolean'
        || matrix?.station_id !== station.id || !Array.isArray(matrix.items)
        || !Number.isSafeInteger(configuration.station_latest_version) || configuration.station_latest_version < 0
        || !(configuration.default_rule === null || typeof configuration.default_rule === 'object' && configuration.default_rule?.id)) {
        throw new Error('站点配置响应不完整，请重新加载');
      }
      setLoaded({ configuration, matrix });
    } catch (error) {
      if (current === generation.current) setLoadError(errorText(error, '站点配置读取失败'));
    } finally {
      if (current === generation.current) setLoading(false);
    }
  }, [station.id]);

  useEffect(() => { void load(); return () => { generation.current++; }; }, [load]);
  useEffect(() => {
    context.current = targetKey;
    modalGeneration.current++;
    setPricingOpen(false); setPackageOpen(false); setCandidates([]); setPackages([]);
    setSelectedTemplate(null); setSelectedPackage(null); setPricingError(''); setPackageError('');
    return () => {
      if (context.current === targetKey) context.current = '';
      modalGeneration.current++;
      confirmations.current.forEach(confirmation => confirmation.destroy());
      confirmations.current = [];
    };
  }, [targetKey]);

  const configuration = loaded?.configuration.station_id === station.id ? loaded.configuration : null;
  const rows = configuration ? loaded?.matrix.items || [] : [];
  const device = targetDevice ? rows.find(row => row.device_id === targetDevice) : undefined;
  const ready = !!configuration && !loading && !loadError && (!targetDevice || !!device);
  const canManageTarget = !!targetDevice || configuration?.can_manage_default === true;
  const canCreate = ready && canManageTarget && configuration.permissions.includes('pricing.rule.create');
  const canUpdate = ready && canManageTarget && configuration.permissions.includes('pricing.rule.update');
  const canPublish = canCreate && station.status === 'active';
  const busy = !!saving;
  const defaultRule = configuration?.default_rule;
  const ownRule = !!device?.own_rule_id;
  const currentSpec = targetDevice ? device?.spec_json : defaultRule?.spec_json;
  const currentName = targetDevice ? device?.template_name : defaultRule?.name;
  const currentVersion = targetDevice ? (ownRule ? device?.own_version : device?.station_version) : defaultRule?.version;
  const currentTemplate = targetDevice ? (ownRule ? device?.template_id : undefined) : defaultRule?.template_id;
  const expectedVersion = targetDevice ? device?.own_latest_version || 0 : configuration?.station_latest_version || 0;
  const allOffers = configuration?.offers.filter(offer => offer.station_id === station.id) || [];
  const exactOffers = allOffers.filter(offer => targetDevice ? offer.device_id === targetDevice : isStationOffer(offer));
  const activeOwnOffers = exactOffers.filter(offer => offer.status === 'active');
  // 设备上架的同模板套餐覆盖站点那份，其他通用套餐仍然可用。
  const activeOffers = targetDevice
    ? [...activeOwnOffers, ...allOffers.filter(offer => offer.status === 'active' && isStationOffer(offer)
      && !activeOwnOffers.some(own => own.package_template_id != null && own.package_template_id === offer.package_template_id))]
    : activeOwnOffers;
  const selectedCandidate = candidates.find(candidate => candidate.id === selectedTemplate);
  const selectedPackageTemplate = packages.find(template => template.id === selectedPackage);
  const existingPackage = exactOffers.find(offer => offer.package_template_id === selectedPackage);
  const packagePreview = existingPackage?.status === 'disabled' ? existingPackage : selectedPackageTemplate;

  const chooseDevice = (id: string | null) => { setLocalDevice(id); onDeviceChange?.(id); };
  const refreshAfterWrite = async (expectedContext: string) => {
    if (context.current !== expectedContext) return;
    onChanged?.();
    await load();
  };
  const publicationNotice = (result: Publication, text: string, deviceBilled = false) => {
    if (result.switch_pending) message.success(`${text}，设备切换待下发${result.switch_task_id ? `（任务 #${result.switch_task_id}）` : ''}`);
    else if (result.replayed && deviceBilled) message.success(`${text}，请在下发记录确认设备切换状态`);
    else message.success(text);
  };

  const loadCandidates = async () => {
    const current = ++modalGeneration.current, expectedContext = targetKey;
    setCandidateLoading(true); setCandidateError(''); setCandidates([]);
    try {
      const result = await apiGet<{ items: Candidate[] }>('/api/v1/admin/settings/pricing-template-candidates', {
        station_id: station.id, device_id: targetDevice || undefined, preserve_device_overrides: !targetDevice,
      });
      if (current !== modalGeneration.current || context.current !== expectedContext) return;
      if (!Array.isArray(result?.items)) throw new Error('计费模板候选响应不完整');
      setCandidates(result.items);
    } catch (error) {
      if (current === modalGeneration.current && context.current === expectedContext) setCandidateError(errorText(error, '计费模板读取失败'));
    } finally {
      if (current === modalGeneration.current && context.current === expectedContext) setCandidateLoading(false);
    }
  };
  const openPricing = () => {
    if (!canPublish || busy) return;
    setSelectedTemplate(null); setPricingError(''); pricingIntent.current = null;
    setPricingOpen(true); void loadCandidates();
  };
  const savePricing = async () => {
    if (!canPublish || busy || candidateLoading || candidateError || !selectedCandidate || selectedCandidate.unavailable_reason
      || selectedCandidate.id === currentTemplate || selectedCandidate.status !== 'active') return;
    const expectedContext = targetKey;
    const body = { station_id: station.id, device_id: targetDevice || '', expected_version: expectedVersion, preserve_device_overrides: !targetDevice };
    const requestId = requestIdFor(pricingIntent, JSON.stringify({ template_id: selectedCandidate.id, ...body }));
    setSaving('pricing'); setPricingError('');
    try {
      const result = await apiPost<Publication>(`/api/v1/admin/settings/pricing-templates/${selectedCandidate.id}/apply`, { ...body, request_id: requestId });
      if (context.current !== expectedContext) return;
      publicationNotice(result, '计费配置已保存', !!selectedCandidate.spec?.mode && !isServerBilled(selectedCandidate.spec.mode)); setPricingOpen(false);
      await refreshAfterWrite(expectedContext);
    } catch (error) {
      if (context.current === expectedContext) setPricingError(errorText(error, '计费配置保存失败'));
    } finally { setSaving(null); }
  };

  const loadPackages = async () => {
    const current = ++modalGeneration.current, expectedContext = targetKey;
    setPackageLoading(true); setPackageLoadError(''); setPackages([]);
    try {
      const result = await apiGet<{ items: PackageTemplate[] }>('/api/v1/admin/settings/package-templates');
      if (current !== modalGeneration.current || context.current !== expectedContext) return;
      if (!Array.isArray(result?.items)) throw new Error('套餐模板响应不完整');
      setPackages(result.items.filter(template => template.status === 'active'));
    } catch (error) {
      if (current === modalGeneration.current && context.current === expectedContext) setPackageLoadError(errorText(error, '套餐模板读取失败'));
    } finally {
      if (current === modalGeneration.current && context.current === expectedContext) setPackageLoading(false);
    }
  };
  const openPackage = () => {
    if (!canPublish || busy) return;
    setSelectedPackage(null); setPackageError(''); packageIntent.current = null;
    setPackageOpen(true); void loadPackages();
  };
  const applyPackage = async () => {
    if (!canPublish || busy || packageLoading || packageLoadError || !selectedPackageTemplate || existingPackage?.status === 'active') return;
    const expectedContext = targetKey;
    const body = { station_id: station.id, device_id: targetDevice || '' };
    const requestId = requestIdFor(packageIntent, JSON.stringify({ template_id: selectedPackageTemplate.id, ...body }));
    setSaving('package'); setPackageError('');
    try {
      const result = await apiPost<{ replayed?: boolean; relisted?: boolean }>(`/api/v1/admin/settings/package-templates/${selectedPackageTemplate.id}/apply`, { ...body, request_id: requestId });
      if (context.current !== expectedContext) return;
      message.success(result.relisted ? '原套餐已重新上架' : result.replayed ? '该套餐已在售' : '套餐已上架');
      setPackageOpen(false); await refreshAfterWrite(expectedContext);
    } catch (error) {
      if (context.current === expectedContext) setPackageError(errorText(error, '套餐上架失败'));
    } finally { setSaving(null); }
  };

  const disableOffer = (offer: Offer) => {
    if (!canUpdate || busy || targetDevice && isStationOffer(offer)) return;
    const expectedContext = targetKey;
    const confirmation = modal.confirm({
      title: `下架「${offer.name}」`, okText: '下架', cancelText: '取消', okButtonProps: { danger: true },
      content: isStationOffer(offer)
        ? '沿用此通用套餐的设备将不再提供它；设备单独上架的套餐保留。已支付订单保留原有条款。'
        : `设备 ${offer.device_id} 将不再提供这份独立套餐；同模板的站点通用套餐如在售，将恢复显示。已支付订单保留原有条款。`,
      onOk: async () => {
        if (context.current !== expectedContext) throw new Error('当前配置范围已变化，请重新打开操作');
        setSaving('disable');
        try {
          await apiPost(`/api/v1/admin/settings/charge-offers/${offer.id}/disable`);
          if (context.current === expectedContext) { message.success('套餐已下架'); await refreshAfterWrite(expectedContext); }
        } catch (error) { message.error(errorText(error, '套餐下架失败')); throw error; }
        finally { setSaving(null); }
      },
    });
    confirmations.current.push(confirmation);
  };
  const resetPricing = () => {
    if (!canUpdate || busy || !targetDevice || !ownRule || !defaultRule || station.status !== 'active') return;
    const expectedContext = targetKey, selectedDevice = targetDevice, selectedStation = station.id;
    const confirmation = modal.confirm({
      title: '恢复站点默认计费', okText: '恢复默认', cancelText: '取消',
      content: `设备 ${selectedDevice} 将沿用站点默认计费「${defaultRule.name}」。该设备单独上架的套餐保留；已支付订单保留原有条款。`,
      onOk: async () => {
        if (context.current !== expectedContext) throw new Error('当前配置范围已变化，请重新打开操作');
        setSaving('reset');
        try {
          const result = await apiPost<Publication>('/api/v1/admin/settings/device-pricing/reset', { station_id: selectedStation, device_id: selectedDevice, keep_device_offers: true });
          if (context.current === expectedContext) { publicationNotice(result, '已恢复站点默认计费'); await refreshAfterWrite(expectedContext); }
        } catch (error) { message.error(errorText(error, '恢复默认失败')); throw error; }
        finally { setSaving(null); }
      },
    });
    confirmations.current.push(confirmation);
  };

  if (loading) return <div style={{ padding: 32, textAlign: 'center' }}><Spin tip="正在读取站点计费与套餐"><div style={{ minHeight: 32 }} /></Spin></div>;
  if (loadError) return <LoadError title="站点计费与套餐加载失败" detail={loadError} onRetry={() => void load()} />;
  if (!configuration) return null;
  if (targetDevice && !device) return <Space direction="vertical" style={{ width: '100%' }}>
    <Alert type="error" showIcon message="该设备不在当前站点中，请重新选择设备。" />
    <Button onClick={() => chooseDevice(null)}>返回站点默认</Button>
  </Space>;

  return <Space direction="vertical" size="middle" style={{ width: '100%' }}>
    <Space wrap>
      <Typography.Text strong>{targetDevice ? `设备 ${targetDevice} 的计费与套餐` : `${station.name} · 站点默认配置`}</Typography.Text>
      {targetDevice && <Button onClick={() => chooseDevice(null)} disabled={busy}>返回站点默认</Button>}
      {(onDeviceChange || deviceId === undefined) && <Select aria-label="配置范围" style={{ width: 280 }} value={targetDevice || ''} disabled={busy}
        showSearch optionFilterProp="label" onChange={value => chooseDevice(value || null)} options={[
          { value: '', label: '站点默认' }, ...rows.map(row => ({ value: row.device_id, label: `设备 ${row.device_id}${row.model ? ` · ${row.model}` : ''}` })),
        ]} />}
      <Button onClick={() => void load()} disabled={busy}>刷新配置</Button>
    </Space>
    {station.status !== 'active' && <Alert type="info" showIcon message="本站点当前未运营，现有配置暂不用于充电；暂不能发布计费或上架套餐。" />}
    {!targetDevice && !configuration.can_manage_default && <Alert type="info" showIcon message="当前账号可查看站点默认配置，请选择设备管理其独立计费与套餐。" />}
    <Card size="small" title={targetDevice ? '当前设备计费' : '站点默认计费'} extra={canCreate && <Button type="primary" onClick={openPricing} disabled={!canPublish || busy}>选择计费模板</Button>}>
      {currentSpec?.mode ? <Space direction="vertical" style={{ width: '100%' }}>
        <Space wrap><Typography.Text strong>{currentName || modeLabel(currentSpec.mode)}</Typography.Text>
          <Tag color={targetDevice && ownRule ? 'blue' : 'default'}>{targetDevice && ownRule ? '设备独立' : '站点默认'}</Tag>
          {currentVersion != null && <Typography.Text type="secondary">规则 v{currentVersion}</Typography.Text>}
        </Space>
        <Typography.Paragraph style={{ marginBottom: 0 }}>{describeSpec(currentSpec)}</Typography.Paragraph>
      </Space> : <Empty image={Empty.PRESENTED_IMAGE_SIMPLE} description={targetDevice ? '该设备尚无可用计费规则' : '尚未配置站点默认计费'} />}
      {targetDevice && <Space direction="vertical" style={{ width: '100%', marginTop: 12 }}>
        {ownRule ? <>
          <Typography.Text type="secondary">此设备已单独定价，站点默认计费变更会保留它的独立规则。</Typography.Text>
          {canUpdate && <Button onClick={resetPricing} disabled={busy || !defaultRule || station.status !== 'active'}>恢复站点默认计费</Button>}
          {!defaultRule && <Typography.Text type="secondary">请先设置站点默认计费，再恢复设备默认。</Typography.Text>}
        </> : <Typography.Text type="secondary">此设备沿用站点默认。为它选择模板后，将建立独立计费规则。</Typography.Text>}
      </Space>}
    </Card>
    <Card size="small" title={targetDevice ? '该设备可选套餐' : '站点通用在售套餐'} extra={canCreate && <Button onClick={openPackage} disabled={!canPublish || busy}>添加套餐</Button>}>
      <Typography.Paragraph type="secondary">{targetDevice
        ? '设备独立套餐优先；同模板的站点套餐被覆盖，其他通用套餐继续可选。'
        : '通用套餐供本站点设备使用；设备单独上架的同模板套餐优先。计费模板与套餐分别管理。'}</Typography.Paragraph>
      <Table<Offer> rowKey="id" size="small" dataSource={activeOffers} pagination={false} scroll={{ x: 760 }} locale={{ emptyText: '暂无在售套餐' }} columns={[
        { title: '名称', dataIndex: 'name' },
        { title: '来源', render: (_: unknown, offer: Offer) => <Tag color={isStationOffer(offer) ? 'default' : 'blue'}>{isStationOffer(offer) ? '站点通用' : '设备独立'}</Tag> },
        { title: '类型', dataIndex: 'mode', render: offerKind },
        { title: '条款', render: (_: unknown, offer: Offer) => offer.mode === 'package' ? `${offer.duration_minutes} 分钟` : money(offer.price_cents) },
        { title: '最低消费', dataIndex: 'min_charge_cents', render: money },
        { title: '刷卡默认', dataIndex: 'card_default', render: (value: boolean) => value ? '是' : '否' },
        { title: '操作', render: (_: unknown, offer: Offer) => targetDevice && isStationOffer(offer)
          ? <Button type="link" onClick={() => chooseDevice(null)} disabled={busy}>管理通用套餐</Button>
          : canUpdate && <Button type="link" danger onClick={() => disableOffer(offer)} disabled={busy}>下架</Button> },
      ]} />
    </Card>
    {!targetDevice && <Card size="small" title="设备例外配置">
      <Table<DevicePricing> rowKey="device_id" size="small" dataSource={rows} scroll={{ x: 650 }} pagination={{ pageSize: 10, showSizeChanger: true }} locale={{ emptyText: '本站点暂无设备' }} columns={[
        { title: '设备', dataIndex: 'device_id' },
        { title: '当前计费', render: (_: unknown, row: DevicePricing) => row.spec_json?.mode ? modeLabel(row.spec_json.mode) : '未配置' },
        { title: '计费来源', render: (_: unknown, row: DevicePricing) => <Tag color={row.own_rule_id ? 'blue' : 'default'}>{row.own_rule_id ? '设备独立' : '站点默认'}</Tag> },
        { title: '独立套餐', render: (_: unknown, row: DevicePricing) => allOffers.filter(offer => offer.device_id === row.device_id && offer.status === 'active').length },
        { title: '操作', render: (_: unknown, row: DevicePricing) => <Button type="link" disabled={busy} onClick={() => chooseDevice(row.device_id)}>计费与套餐</Button> },
      ]} />
    </Card>}

    <Modal title={targetDevice ? `设置设备 ${targetDevice} 的独立计费` : '设置站点默认计费'} open={pricingOpen} width={720}
      onCancel={() => { if (!busy) { modalGeneration.current++; setPricingOpen(false); } }} closable={!busy} maskClosable={!busy}
      confirmLoading={saving === 'pricing'} onOk={() => void savePricing()} okText="确认应用" cancelText="取消"
      okButtonProps={{ disabled: !canPublish || !selectedCandidate || !!selectedCandidate.unavailable_reason || selectedCandidate.id === currentTemplate || candidateLoading || !!candidateError || busy }}>
      <Space direction="vertical" style={{ width: '100%' }} size="middle">
        <Alert type="info" showIcon message={targetDevice ? `仅为设备 ${targetDevice} 发布独立计费规则；已支付订单保留原有条款。` : '更新站点默认计费，保留已有设备独立计费规则；已支付订单保留原有条款。'} />
        {candidateError && <LoadError title="计费模板加载失败" detail={candidateError} onRetry={() => void loadCandidates()} />}
        {pricingError && <Alert type="error" showIcon message={pricingError} />}
        <Select aria-label="选择计费模板" placeholder="选择可用于当前范围的模板" style={{ width: '100%' }} loading={candidateLoading} value={selectedTemplate ?? undefined}
          disabled={busy || !ready} onChange={value => { setSelectedTemplate(value); setPricingError(''); }} options={candidates.map(candidate => {
            const reason = candidate.unavailable_reason || (candidate.id === currentTemplate ? '当前范围已应用' : '');
            return { value: candidate.id, disabled: !!reason || candidate.status !== 'active', label: `${candidate.name} · ${modeLabel(candidate.spec?.mode || '')}${reason ? `（${reason}）` : ''}` };
          })} />
        {selectedCandidate && <Descriptions size="small" column={1} bordered items={[
          { key: 'target', label: '应用范围', children: targetDevice ? `${station.name} / 设备 ${targetDevice}` : `${station.name} / 站点默认` },
          { key: 'before', label: '当前规则', children: currentSpec?.mode ? `${currentName || modeLabel(currentSpec.mode)}（规则 v${currentVersion ?? 0}）` : '未配置' },
          { key: 'next', label: '模板预览', children: describeSpec(selectedCandidate.spec) },
          { key: 'inheritance', label: '影响', children: targetDevice ? '该设备不再沿用站点默认；套餐继续单独管理。' : `更新默认规则；保留 ${rows.filter(row => row.own_rule_id).length} 台设备的独立规则。套餐继续单独管理。` },
        ]} />}
      </Space>
    </Modal>

    <Modal title={targetDevice ? `为设备 ${targetDevice} 添加套餐` : '添加站点通用套餐'} open={packageOpen} width={720}
      onCancel={() => { if (!busy) { modalGeneration.current++; setPackageOpen(false); } }} closable={!busy} maskClosable={!busy}
      confirmLoading={saving === 'package'} onOk={() => void applyPackage()} okText={existingPackage?.status === 'disabled' ? '重新上架原套餐' : '确认上架'} cancelText="取消"
      okButtonProps={{ disabled: !canPublish || !selectedPackageTemplate || existingPackage?.status === 'active' || packageLoading || !!packageLoadError || busy }}>
      <Space direction="vertical" style={{ width: '100%' }} size="middle">
        <Alert type="info" showIcon message={targetDevice ? `上架范围：${station.name} / 设备 ${targetDevice}。设备同模板套餐优先于站点通用套餐。` : `上架范围：${station.name} / 站点通用套餐。设备单独上架的同模板套餐继续优先。`} />
        {packageLoadError && <LoadError title="套餐模板加载失败" detail={packageLoadError} onRetry={() => void loadPackages()} />}
        {packageError && <Alert type="error" showIcon message={packageError} />}
        <Select aria-label="选择套餐模板" placeholder="选择套餐模板" style={{ width: '100%' }} loading={packageLoading} value={selectedPackage ?? undefined}
          disabled={busy || !ready} onChange={value => { setSelectedPackage(value); setPackageError(''); }} options={packages.map(template => {
            const alreadyActive = exactOffers.some(offer => offer.package_template_id === template.id && offer.status === 'active');
            const oldOffer = exactOffers.some(offer => offer.package_template_id === template.id && offer.status === 'disabled');
            return { value: template.id, disabled: alreadyActive, label: `${template.name}${alreadyActive ? '（当前范围已在售）' : oldOffer ? '（可恢复原套餐）' : ''}` };
          })} />
        {existingPackage?.status === 'disabled' && <Alert type="warning" showIcon message="此范围已有下架记录。本次恢复原套餐条款；模板后来修改的值不会覆盖它。下方展示原记录的实际条款。" />}
        {packagePreview && offerTerms(packagePreview)}
      </Space>
    </Modal>
  </Space>;
}
