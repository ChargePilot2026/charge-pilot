import { useEffect, useState } from 'react';
import { Reconciliation, Settlements, Withdrawals } from './FinanceOps';
import { Button, Form, Input, Modal, Space, Table, Tabs, Tag, Typography, message } from 'antd';
import { ReloadOutlined } from '@ant-design/icons';
import { apiGet, apiPost } from '../api/client';
import Refunds from './Refunds';
import MeterReviews from './MeterReviews';
import WalletRisks from './WalletRisks';
import { LoadError } from '../components/LoadError';

const { Title, Text } = Typography;
const invoiceEndpoint = '/api/v1/admin/billing/invoices';

interface Invoice {
  invoice_request_id: number;
  invoice_no: string;
  user_id: number;
  biz_type: string;
  biz_id: number;
  total_cents: number;
  invoice_type: string;
  title: string;
  tax_no: string | null;
  email: string | null;
  review_status: string;
  queue_review_status: string;
  queue_reject_reason: string | null;
  first_reviewer_id: string | null;
  second_reviewer_id: string | null;
  reject_reason: string | null;
  invoice_url: string | null;
  created_at: string;
}

const statusLabel: Record<string, string> = { pending: '待首次审核', awaiting_second: '待第二人复核', approved: '已复核并开具', rejected: '已拒绝', issued: '已开具' };

export default function BillingPage() {
  const [invoices, setInvoices] = useState<Invoice[]>([]);
  const [loadingInvoices, setLoadingInvoices] = useState(false);
  const [invoiceError, setInvoiceError] = useState('');
  const [selected, setSelected] = useState<Invoice | null>(null);
  const [decision, setDecision] = useState<'approve' | 'reject' | null>(null);
  const [saving, setSaving] = useState(false);
  const [form] = Form.useForm();
  let currentRole = '';
  let currentAdminId = '';
  try { const profile = JSON.parse(localStorage.getItem('cp_admin') || 'null'); currentAdminId = String(profile?.admin_user_id || ''); currentRole = profile?.role || ''; } catch { currentAdminId = ''; }

  const loadInvoices = async () => {
    setLoadingInvoices(true);
    setInvoiceError('');
    try {
      const result = await apiGet<{ items: Invoice[] }>(invoiceEndpoint);
      setInvoices(Array.isArray(result?.items) ? result.items : []);
    } catch (error: any) {
      setInvoiceError(error?.message || '发票队列读取失败');
    } finally {
      setLoadingInvoices(false);
    }
  };

  useEffect(() => {
    void loadInvoices();
  }, []);

  const openDecision = (invoice: Invoice, next: 'approve' | 'reject') => {
    setSelected(invoice);
    setDecision(next);
    form.resetFields();
    if (next === 'approve' && invoice.invoice_url) form.setFieldsValue({ invoice_url: invoice.invoice_url });
  };

  const submitDecision = async () => {
    if (!selected || !decision || saving) return;
    try {
      const values = await form.validateFields();
      setSaving(true);
      let result: { review_status?: string } | null = null;
      if (decision === 'approve') {
        result = await apiPost<{ review_status?: string }>(`${invoiceEndpoint}/${selected.invoice_request_id}/approve`, { invoice_url: values.invoice_url.trim() });
      } else {
        await apiPost(`${invoiceEndpoint}/${selected.invoice_request_id}/reject`, { reason: values.reason.trim() });
      }
      if (decision === 'approve') {
        message.success(result?.review_status === 'awaiting_second' ? '首次审核已保存，等待另一名财务复核' : '第二人复核完成，发票已登记开具');
      } else message.success('已拒绝发票申请');
      setSelected(null);
      setDecision(null);
      await loadInvoices();
    } catch (error: any) {
      if (!error?.errorFields) message.error(error?.message || '审核失败');
    } finally {
      setSaving(false);
    }
  };

  return (
    <div className="page-container">
      <Title level={3}>财务</Title>
      <Tabs items={[
        { key: 'wallet-risks', label: '钱包风控审核', children: <WalletRisks /> },
        { key: 'settlements', label: '分账明细', children: <Settlements /> },
        { key: 'withdrawals', label: '提现打款', children: <Withdrawals /> },
        { key: 'reconciliation', label: '对账', children: <Reconciliation /> },
        { key: 'refunds', label: '退款审核', children: <Refunds /> },
        { key: 'meter-reviews', label: '计量核实', children: <MeterReviews /> },
        {
          key: 'invoices', label: '发票审核', children: (
            <>
              <Space style={{ marginBottom: 12 }}>
                <Button icon={<ReloadOutlined />} onClick={() => void loadInvoices()} loading={loadingInvoices}>刷新</Button>
                {invoiceError && <LoadError title="发票队列加载失败" detail={invoiceError} onRetry={() => void loadInvoices()} />}
              </Space>
              <Table rowKey="invoice_request_id" loading={loadingInvoices} dataSource={invoices} scroll={{ x: 1120 }} columns={[
                { title: '申请单', dataIndex: 'invoice_no', width: 190 },
                { title: '抬头 / 税号', render: (_: unknown, row: Invoice) => <><div>{row.title}</div><Text type="secondary">{row.tax_no || '个人抬头'}</Text></> },
                { title: '用户', dataIndex: 'user_id', width: 90 },
                { title: '金额', dataIndex: 'total_cents', width: 110, render: (cents: number) => `¥${(cents / 100).toFixed(2)}` },
                { title: '类型', dataIndex: 'invoice_type', width: 130, render: (type: string) => type === 'vat_special' ? '增值税专用' : '普通发票' },
                { title: '状态', width: 130, render: (_: unknown, row: Invoice) => { const status = row.queue_review_status || row.review_status; return <Tag color={status === 'pending' ? 'gold' : status === 'awaiting_second' ? 'processing' : status === 'rejected' ? 'red' : 'green'}>{statusLabel[status] || status}</Tag>; } },
                { title: '审核记录', width: 180, render: (_: unknown, row: Invoice) => <><div>首审：{row.first_reviewer_id || '—'}</div><Text type="secondary">复核：{row.second_reviewer_id || '—'}</Text></> },
                { title: '拒绝原因', width: 180, render: (_: unknown, row: Invoice) => row.queue_reject_reason || row.reject_reason },
                { title: '操作', fixed: 'right', width: 190, render: (_: unknown, row: Invoice) => {
                  const status = row.queue_review_status || row.review_status;
                  if (['pending','awaiting_second'].includes(status) && currentRole !== 'customer_finance') return <Text type="secondary">等待财务审核</Text>;
                  if (status === 'pending') return <Space><Button type="link" onClick={() => openDecision(row, 'approve')}>首次审核</Button><Button type="link" danger onClick={() => openDecision(row, 'reject')}>拒绝</Button></Space>;
                  if (status === 'awaiting_second') return row.first_reviewer_id === currentAdminId
                    ? <Text type="secondary">等待其他财务复核</Text>
                    : <Space><Button type="link" onClick={() => openDecision(row, 'approve')}>第二人复核</Button><Button type="link" danger onClick={() => openDecision(row, 'reject')}>拒绝</Button></Space>;
                  return row.invoice_url ? <a href={row.invoice_url} target="_blank" rel="noreferrer">查看发票</a> : null;
                } },
              ]} />
            </>
          ),
        },
      ]} />
      <Modal
        title={decision === 'approve' ? (selected?.queue_review_status === 'awaiting_second' ? '第二人复核' : '发票首次审核') : '拒绝发票申请'}
        open={!!selected && !!decision}
        onCancel={() => { setSelected(null); setDecision(null); }}
        onOk={() => void submitDecision()}
        confirmLoading={saving}
        destroyOnHidden
      >
        {selected && <div style={{ marginBottom: 16 }}>
          <div>{selected.invoice_no} · {selected.title}</div>
          <Text type="secondary">用户 {selected.user_id} · ¥{(selected.total_cents / 100).toFixed(2)}{selected.first_reviewer_id ? ` · 首审人 ${selected.first_reviewer_id}` : ''}</Text>
        </div>}
        <Form form={form} layout="vertical">
          {decision === 'approve'
            ? <Form.Item name="invoice_url" label="已开具的 HTTPS 发票链接（复核时须与首审一致）" rules={[{ required: true, type: 'url', message: '请填写有效的 HTTPS 链接' }, { validator: async (_, value) => { if (value && !value.startsWith('https://')) throw new Error('链接必须使用 HTTPS'); } }]}><Input maxLength={512} placeholder="https://..." /></Form.Item>
            : <Form.Item name="reason" label="拒绝原因" rules={[{ required: true, whitespace: true, max: 255 }]}><Input.TextArea rows={3} maxLength={255} showCount /></Form.Item>}
        </Form>
      </Modal>
    </div>
  );
}
