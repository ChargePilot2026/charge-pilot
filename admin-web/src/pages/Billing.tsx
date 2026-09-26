import { useEffect, useState } from 'react';
import { Tabs, Table, Tag, Typography } from 'antd';
import { apiGet } from '../api/client';

const { Title } = Typography;

export default function BillingPage() {
  const [settlements, setSettlements] = useState<any[]>([]);
  const [refunds, setRefunds] = useState<any[]>([]);
  const [invoices, setInvoices] = useState<any[]>([]);

  useEffect(() => {
    apiGet<{ items: any[] }>('/api/v1/admin/billing/settlements').then(d => setSettlements(d.items || [])).catch(() => {});
    apiGet<{ items: any[] }>('/api/v1/admin/billing/refunds').then(d => setRefunds(d.items || [])).catch(() => {});
    apiGet<{ items: any[] }>('/api/v1/admin/billing/invoices').then(d => setInvoices(d.items || [])).catch(() => {});
  }, []);

  return (
    <div className="page-container">
      <Title level={3}>财务</Title>
      <Tabs
        items={[
          {
            key: 'settlements',
            label: '分账出账',
            children: (
              <Table rowKey="id" dataSource={settlements}
                columns={[
                  { title: '出账单号', dataIndex: 'settlement_no', width: 220 },
                  { title: '期间', render: (_: any, r: any) => `${r.period_start} - ${r.period_end}` },
                  { title: '金额(分)', dataIndex: 'total_cents', width: 120 },
                  { title: '状态', dataIndex: 'status', render: (s: string) => <Tag color="blue">{s}</Tag> },
                ]}
              />
            ),
          },
          {
            key: 'refunds',
            label: '退款审核',
            children: (
              <Table rowKey="id" dataSource={refunds}
                columns={[
                  { title: '退款单号', dataIndex: 'refund_no', width: 220 },
                  { title: '金额(分)', dataIndex: 'refund_cents' },
                  { title: '状态', dataIndex: 'status' },
                  { title: '原因', dataIndex: 'reason' },
                ]}
              />
            ),
          },
          {
            key: 'invoices',
            label: '发票审核',
            children: (
              <Table rowKey="invoice_request_id" dataSource={invoices}
                columns={[
                  { title: '申请 ID', dataIndex: 'invoice_request_id', width: 120 },
                  { title: '状态', dataIndex: 'review_status' },
                  { title: '拒绝原因', dataIndex: 'reject_reason' },
                ]}
              />
            ),
          },
        ]}
      />
    </div>
  );
}