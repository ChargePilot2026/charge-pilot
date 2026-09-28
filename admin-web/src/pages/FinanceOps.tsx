import { useCallback, useEffect, useState, type ReactNode } from 'react';
import { Alert, App, Button, DatePicker, Descriptions, Form, Input, Modal, Select, Space, Table, Tag, Typography } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { adminSession, apiGet, apiPost } from '../api/client';

const { Text, Paragraph } = Typography;

const endpoint = '/api/v1/admin/billing';

interface Party {
  party_id: number;
  party_code: string;
  party_name: string;
  ratio_bp: number;
  electric_cents: number;
  service_cents: number;
  amount_cents: number;
  status: string;
}

interface Settlement {
  id: number;
  settlement_no: string;
  order_no: string;
  mode: string;
  split_template_code: string | null;
  status: string;
  total_cents: number;
  electric_cents: number;
  service_cents: number;
  split_pool_cents: number;
  split_pool_excluded_electric_cents: number;
  created_at: string;
  parties: Party[];
}

interface Withdraw {
  id: number;
  withdraw_no: string;
  party_id: number;
  party_code: string;
  amount_cents: number;
  bank_account: string | null;
  bank_name: string | null;
  status: string;
  reviewed_by: number | null;
  reject_reason: string | null;
  paid_at: string | null;
  note: string | null;
  created_at: string;
}

interface ReconcileDiff {
  ref: string;
  internal_cents: number;
  channel_cents: number;
  reason: string;
}

interface Reconcile {
  id: number;
  reconcile_type: string;
  reconcile_date: string;
  internal_count: number;
  wechat_count: number;
  diff_count: number;
  internal_cents: number;
  wechat_cents: number;
  diff_cents: number;
  diffs: ReconcileDiff[];
  resolved: boolean;
}

const money = (cents: number | null | undefined) => `¥${((cents ?? 0) / 100).toFixed(2)}`;
const settlementStatus: Record<string, { label: string; color: string }> = {
  pending: { label: '待确认', color: 'gold' },
  confirmed: { label: '已确认', color: 'blue' },
  paid: { label: '已打款', color: 'green' },
  failed: { label: '失败', color: 'red' },
};
const withdrawStatus: Record<string, { label: string; color: string }> = {
  pending: { label: '待审核', color: 'gold' },
  approved: { label: '待打款', color: 'blue' },
  rejected: { label: '已拒绝', color: 'red' },
  paid: { label: '已打款', color: 'green' },
  failed: { label: '失败', color: 'red' },
};
const reconcileType: Record<string, string> = {
  wechat_refund: '微信退款', wechat_pay: '微信支付', split: '分账出账', withdraw: '提现打款',
};

/** Settlements shows the allocations actually written for each calculated fee. */
export function Settlements() {
  const [rows, setRows] = useState<Settlement[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [detail, setDetail] = useState<Settlement | null>(null);

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const query = new URLSearchParams({ page: String(page), page_size: '20' });
      if (status) query.set('status', status);
      const data = await apiGet<{ items: Settlement[]; total: number }>(`${endpoint}/settlements?${query}`);
      setRows(data.items || []);
      setTotal(data.total || 0);
    } catch (e: any) {
      setError(e?.message || '分账记录读取失败');
    } finally {
      setLoading(false);
    }
  }, [page, status]);

  useEffect(() => { void load(); }, [load]);

  return (
    <div>
      <Space style={{ marginBottom: 12 }} wrap>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        <SelectStatus value={status} onChange={(v) => { setStatus(v); setPage(1); }}
          options={Object.entries(settlementStatus).map(([value, meta]) => ({ value, label: meta.label }))} />
        {error && <Text type="danger">{error}</Text>}
      </Space>
      {rows.length === 0 && !loading ? (
        <Alert type="info" showIcon message="暂无分账记录" description="订单计费完成后会按站点分账模板自动生成分账明细。" />
      ) : (
        <Table<Settlement>
          rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 1100 }}
          pagination={{ current: page, pageSize: 20, total, showSizeChanger: false, onChange: setPage }}
          columns={[
            { title: '分账单号', dataIndex: 'settlement_no', width: 190 },
            { title: '订单号', dataIndex: 'order_no', width: 180 },
            { title: '模板', dataIndex: 'split_template_code', width: 130, render: (v: string | null) => v || '—' },
            { title: '模式', dataIndex: 'mode', width: 90, render: (v: string) => (v === 'mode_a' ? '全额分账' : '仅服务费') },
            { title: '合计', dataIndex: 'total_cents', width: 110, render: money },
            { title: '分账池', dataIndex: 'split_pool_cents', width: 110, render: money },
            { title: '状态', dataIndex: 'status', width: 100, render: (v: string) => <Tag color={settlementStatus[v]?.color}>{settlementStatus[v]?.label || v}</Tag> },
            { title: '参与方', width: 110, render: (_, row) => <Button type="link" onClick={() => setDetail(row)}>查看 {row.parties?.length || 0} 方</Button> },
          ]}
        />
      )}
      <Modal title="分账明细" open={!!detail} onCancel={() => setDetail(null)} footer={null} width={720}>
        {detail && <Descriptions column={1} size="small" bordered items={[
          { key: 'no', label: '分账单号', children: detail.settlement_no },
          { key: 'order', label: '订单号', children: detail.order_no },
          { key: 'electric', label: '电费', children: money(detail.electric_cents) },
          { key: 'service', label: '服务费', children: money(detail.service_cents) },
          { key: 'pool', label: '进入分账池', children: money(detail.split_pool_cents) },
          ...(detail.split_pool_excluded_electric_cents > 0
            ? [{ key: 'excluded', label: '不参与分账的电费', children: money(detail.split_pool_excluded_electric_cents) }] : []),
        ]} />}
        <Table<Party>
          style={{ marginTop: 16 }} rowKey="party_id" size="small" pagination={false} dataSource={detail?.parties || []}
          columns={[
            { title: '参与方', dataIndex: 'party_name', render: (v: string, r) => <>{v}<Text type="secondary"> （{r.party_code}）</Text></> },
            { title: '比例', dataIndex: 'ratio_bp', width: 90, render: (v: number) => `${(v / 100).toFixed(2)}%` },
            { title: '电费', dataIndex: 'electric_cents', width: 100, render: money },
            { title: '服务费', dataIndex: 'service_cents', width: 100, render: money },
            { title: '金额', dataIndex: 'amount_cents', width: 110, render: money },
          ]}
        />
      </Modal>
    </div>
  );
}

/** Withdrawals covers requesting, approving and paying out a party's earnings. */
export function Withdrawals() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<Withdraw[]>([]);
  const [total, setTotal] = useState(0);
  const [page, setPage] = useState(1);
  const [status, setStatus] = useState('');
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [creating, setCreating] = useState(false);
  const [deciding, setDeciding] = useState<Withdraw | null>(null);
  const [saving, setSaving] = useState(false);
  const [createForm] = Form.useForm();
  const [decideForm] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const query = new URLSearchParams({ page: String(page), page_size: '20' });
      if (status) query.set('status', status);
      const data = await apiGet<{ items: Withdraw[]; total: number }>(`${endpoint}/withdraws?${query}`);
      setRows(data.items || []);
      setTotal(data.total || 0);
    } catch (e: any) {
      setError(e?.message || '提现记录读取失败');
    } finally {
      setLoading(false);
    }
  }, [page, status]);

  useEffect(() => { void load(); }, [load]);

  const submitCreate = async () => {
    const values = await createForm.validateFields();
    setSaving(true);
    try {
      await apiPost(`${endpoint}/withdraws`, {
        request_id: crypto.randomUUID(), party_id: values.party_id, amount_cents: values.amount_cents, note: values.note,
      });
      message.success('提现申请已提交，等待审核');
      setCreating(false);
      createForm.resetFields();
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '提现申请失败');
    } finally {
      setSaving(false);
    }
  };

  const decide = async (approve: boolean) => {
    if (!deciding) return;
    const values = approve ? {} : await decideForm.validateFields();
    setSaving(true);
    try {
      await apiPost(`${endpoint}/withdraws/${encodeURIComponent(deciding.withdraw_no)}/decide`,
        approve ? { approve: true } : { reason: values.reason });
      message.success(approve ? '已审核通过' : '已拒绝该提现申请');
      setDeciding(null);
      decideForm.resetFields();
      await load();
    } catch (e: any) {
      if (!e?.errorFields) message.error(e?.message || '操作失败');
    } finally {
      setSaving(false);
    }
  };

  const pay = async (row: Withdraw) => {
    setSaving(true);
    try {
      await apiPost(`${endpoint}/withdraws/${encodeURIComponent(row.withdraw_no)}/pay`, {});
      message.success('已登记打款');
      await load();
    } catch (e: any) {
      message.error(e?.message || '打款登记失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <Space style={{ marginBottom: 12 }} wrap>
        <Button type="primary" onClick={() => setCreating(true)}>发起提现</Button>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        <SelectStatus value={status} onChange={(v) => { setStatus(v); setPage(1); }}
          options={Object.entries(withdrawStatus).map(([value, meta]) => ({ value, label: meta.label }))} />
        {error && <Text type="danger">{error}</Text>}
      </Space>
      {rows.length === 0 && !loading ? (
        <Alert type="info" showIcon message="暂无提现申请" description="分账状态为已打款的参与方才有可提现余额。" />
      ) : (
        <Table<Withdraw>
          rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 1000 }}
          pagination={{ current: page, pageSize: 20, total, showSizeChanger: false, onChange: setPage }}
          columns={[
            { title: '提现单号', dataIndex: 'withdraw_no', width: 210 },
            { title: '参与方', dataIndex: 'party_code', width: 150 },
            { title: '金额', dataIndex: 'amount_cents', width: 110, render: money },
            { title: '收款账户', width: 180, render: (_, row) => row.bank_account ? <>{row.bank_name}<br /><Text type="secondary">{row.bank_account}</Text></> : '—' },
            { title: '状态', dataIndex: 'status', width: 100, render: (v: string) => <Tag color={withdrawStatus[v]?.color}>{withdrawStatus[v]?.label || v}</Tag> },
            { title: '备注/拒绝原因', width: 160, render: (_, row) => row.reject_reason || row.note || '—' },
            {
              title: '操作', fixed: 'right', width: 160, render: (_, row) => (
                <Space>
                  {row.status === 'pending' && <>
                    <Button type="link" onClick={() => { setDeciding(row); decideForm.resetFields(); }}>审核</Button>
                  </>}
                  {row.status === 'approved' && <Button type="link" onClick={() => void pay(row)} loading={saving}>登记打款</Button>}
                </Space>
              ),
            },
          ]}
        />
      )}
      <Modal title="发起提现申请" open={creating} onCancel={() => setCreating(false)} onOk={() => void submitCreate()}
        confirmLoading={saving} okText="提交申请" cancelText="取消" destroyOnClose>
        <Paragraph type="secondary">只能提现已结算（已打款）的分账金额，服务端会再次校验余额。</Paragraph>
        <Form form={createForm} layout="vertical">
          <Form.Item name="party_id" label="参与方 ID" rules={[{ required: true, message: '请填写参与方 ID' }]}>
            <InputNumberInput placeholder="参与方自增 ID" />
          </Form.Item>
          <Form.Item name="amount_cents" label="金额（分）" rules={[{ required: true, message: '请填写金额' }]}>
            <InputNumberInput placeholder="例如 15000 表示 ¥150.00" min={1} />
          </Form.Item>
          <Form.Item name="note" label="备注"><Input maxLength={255} showCount /></Form.Item>
        </Form>
      </Modal>
      <Modal title={`审核提现 ${deciding?.withdraw_no || ''}`} open={!!deciding} onCancel={() => setDeciding(null)}
        footer={[
          <Button key="reject" danger onClick={() => void decide(false)} loading={saving}>拒绝</Button>,
          <Button key="approve" type="primary" onClick={() => void decide(true)} loading={saving}>通过</Button>,
        ]} destroyOnClose>
        {deciding && <Descriptions size="small" column={1} style={{ marginBottom: 12 }} items={[
          { key: 'a', label: '参与方', children: deciding.party_code },
          { key: 'b', label: '金额', children: money(deciding.amount_cents) },
        ]} />}
        <Form form={decideForm} layout="vertical">
          <Form.Item name="reason" label="拒绝原因（通过时忽略）" rules={[{ max: 255 }]}><Input.TextArea rows={3} maxLength={255} showCount /></Form.Item>
        </Form>
      </Modal>
    </div>
  );
}

/** Reconciliation compares the internal ledger with what the channel reports. */
export function Reconciliation() {
  const { message } = App.useApp();
  const [rows, setRows] = useState<Reconcile[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [running, setRunning] = useState(false);
  const [saving, setSaving] = useState(false);
  const [detail, setDetail] = useState<Reconcile | null>(null);
  const [form] = Form.useForm();

  const load = useCallback(async () => {
    setLoading(true);
    setError('');
    try {
      const data = await apiGet<{ items: Reconcile[] }>(`${endpoint}/reconciles?page=1&page_size=50`);
      setRows(data.items || []);
    } catch (e: any) {
      setError(e?.message || '对账记录读取失败');
    } finally {
      setLoading(false);
    }
  }, []);

  useEffect(() => { void load(); }, [load]);

  const run = async () => {
    const values = await form.validateFields();
    let parsed: { ref: string; amount_cents: number }[] = [];
    try {
      parsed = JSON.parse(values.channel_amounts);
      if (!Array.isArray(parsed)) throw new Error('not an array');
    } catch {
      message.error('渠道对账数据必须是 JSON 数组，例如 [{"ref":"R001","amount_cents":1500}]');
      return;
    }
    setSaving(true);
    try {
      const result = await apiPost<{ diff_count: number }>(`${endpoint}/reconciles`, {
        // The API takes a plain date; the picker returns a dayjs value.
        reconcile_type: values.reconcile_type, date: values.date.format('YYYY-MM-DD'), channel_amounts: parsed,
      });
      message.success(result?.diff_count ? `对账完成，发现 ${result.diff_count} 条差异` : '对账完成，未发现差异');
      form.resetFields();
      await load();
    } catch (e: any) {
      message.error(e?.message || '对账失败');
    } finally {
      setSaving(false);
    }
  };

  const resolve = async (row: Reconcile, resolved: boolean) => {
    setSaving(true);
    try {
      await apiPost(`${endpoint}/reconciles/${row.id}/resolve`, { resolved });
      message.success(resolved ? '已标记为已处理' : '已重新打开');
      await load();
    } catch (e: any) {
      message.error(e?.message || '操作失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div>
      <Paragraph type="secondary">粘贴渠道账单后由服务端逐笔比对，差异原样记录，不自动抹平。</Paragraph>
      <Cardless>
        <Form form={form} layout="inline" style={{ marginBottom: 12, rowGap: 8 }} initialValues={{ reconcile_type: 'wechat_refund' }}>
          <Form.Item name="reconcile_type" label="类型">
            <Select options={Object.entries(reconcileType).map(([value, label]) => ({ value, label }))} />
          </Form.Item>
          <Form.Item name="date" label="日期" rules={[{ required: true, message: '请选择对账日期' }]}>
            <DatePicker style={{ width: 160 }} />
          </Form.Item>
          <Form.Item name="channel_amounts" label="渠道数据（JSON）" rules={[{ required: true, message: '请填写渠道对账数据' }]}
            style={{ flex: 1, minWidth: 320 }}>
            <Input placeholder='[{"ref":"R001","amount_cents":1500}]' />
          </Form.Item>
          <Form.Item><Button type="primary" onClick={() => void run()} loading={saving}>执行对账</Button></Form.Item>
        </Form>
      </Cardless>
      <Space style={{ marginBottom: 12 }}>
        <Button icon={<ReloadOutlined />} onClick={() => void load()} loading={loading}>刷新</Button>
        {error && <Text type="danger">{error}</Text>}
      </Space>
      <Table<Reconcile>
        rowKey="id" loading={loading} dataSource={rows} scroll={{ x: 1000 }} pagination={false}
        columns={[
          { title: '日期', dataIndex: 'reconcile_date', width: 120 },
          { title: '类型', dataIndex: 'reconcile_type', width: 120, render: (v: string) => reconcileType[v] || v },
          { title: '内部笔数', dataIndex: 'internal_count', width: 100 },
          { title: '渠道笔数', dataIndex: 'wechat_count', width: 100 },
          { title: '差异笔数', dataIndex: 'diff_count', width: 100, render: (v: number) => (v > 0 ? <Tag color="red">{v}</Tag> : <Tag color="green">0</Tag>) },
          { title: '差异金额', dataIndex: 'diff_cents', width: 120, render: money },
          { title: '处理状态', dataIndex: 'resolved', width: 110, render: (v: boolean) => v ? <Tag color="green">已处理</Tag> : <Tag color="gold">待处理</Tag> },
          {
            title: '操作', width: 190, render: (_, row) => (<Space>
              <Button type="link" onClick={() => setDetail(row)}>差异明细</Button>
              <Button type="link" onClick={() => void resolve(row, !row.resolved)}>{row.resolved ? '重新打开' : '标记已处理'}</Button>
            </Space>),
          },
        ]}
      />
      <Modal title="差异明细" open={!!detail} onCancel={() => setDetail(null)} footer={null} width={760}>
        <Table<ReconcileDiff>
          rowKey="ref" size="small" pagination={false} dataSource={detail?.diffs || []}
          columns={[
            { title: '流水号', dataIndex: 'ref' },
            { title: '内部金额', dataIndex: 'internal_cents', render: money },
            { title: '渠道金额', dataIndex: 'channel_cents', render: money },
            { title: '原因', dataIndex: 'reason' },
          ]}
        />
      </Modal>
    </div>
  );
}

// Small wrappers keep the tables readable without pulling in more antd surface.
function SelectStatus(props: { value?: string; onChange: (v: string) => void; options: { value: string; label: string }[] }) {
  return <NativeSelect {...props} options={[{ value: '', label: '全部状态' }, ...props.options]} />;
}

function NativeSelect(props: { value?: string; onChange: (v: string) => void; options: { value: string; label: string }[] }) {
  return (
    <select
      value={props.value}
      onChange={(e) => props.onChange(e.target.value)}
      style={{ height: 32, padding: '0 8px', border: '1px solid #d9d9d9', borderRadius: 6, background: '#fff' }}
    >
      {props.options.map((o) => <option key={o.value} value={o.value}>{o.label}</option>)}
    </select>
  );
}

function InputNumberInput(props: { placeholder?: string; min?: number }) {
  return <Input type="number" min={props.min} placeholder={props.placeholder} />;
}

function Cardless({ children }: { children: ReactNode }) {
  return <div style={{ background: '#fafafa', padding: 12, borderRadius: 8, marginBottom: 12 }}>{children}</div>;
}
