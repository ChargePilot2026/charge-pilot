import { Descriptions, Drawer, Space, Spin, Table, Tag, Tooltip, Typography } from 'antd';
import { useEffect, useRef, useState } from 'react';
import dayjs from 'dayjs';
import { adminSession, http, type ApiEnvelope } from '../../api/client';
import { LoadError } from '../../components/LoadError';
import { TABLE_PAGINATION } from '../../utils/tablePagination';
import { businessStatusInfo, paymentStatusInfo } from '../orders/presentation';

export interface ChargeUser {
  id: number;
  openid: string;
  union_id: string | null;
  nickname: string | null;
  avatar_url: string | null;
  gender: string;
  status: string;
  first_seen_at: string;
  last_login_at: string | null;
  inviter_id: number | null;
  created_at: string;
  phone: string;
  phone_bound: boolean;
  balance_cents: number;
  frozen_cents: number;
  order_count: number;
  total_cents: number;
  last_order_at: string | null;
}
interface ChargeUserOrder {
  order_id: number; order_no: string; device_id: string; station_id: number | null;
  status: string; business_status: string; payment_status: string;
  total_cents: number | null; created_at: string; started_at: string | null;
}
interface ChargeUserDetail extends ChargeUser {
  wallet_status: string;
  recent_orders: ChargeUserOrder[];
  coupon_granted: number;
  coupon_unused: number;
  fault_reports: number;
}

export const chargeUserStatuses: Record<string, { label: string; color: string }> = {
  active: { label: '正常', color: 'green' }, frozen: { label: '已冻结', color: 'red' },
};
const walletStatuses: Record<string, string> = { active: '正常', frozen: '已冻结' };
const genders: Record<string, string> = { unknown: '未知', male: '男', female: '女' };
const orderTag = (row: ChargeUserOrder, kind: 'business' | 'payment') => {
  const info = kind === 'business' ? businessStatusInfo(row) : paymentStatusInfo(row);
  const tag = <Tag color={info.color}>{info.label}</Tag>;
  return info.hint ? <Tooltip title={info.hint}>{tag}</Tooltip> : tag;
};
export const chargeUserTime = (value: string | null) => value ? dayjs(value).format('YYYY-MM-DD HH:mm') : '—';
export const chargeUserMoney = (value: number) => `¥${(value / 100).toFixed(2)}`;
export const chargeUserStatusTag = (value: string) => <Tag color={chargeUserStatuses[value]?.color}>{chargeUserStatuses[value]?.label || value}</Tag>;
export const chargeUserName = (chargeUser: Pick<ChargeUser, 'nickname' | 'id'>) => chargeUser.nickname || `用户 #${chargeUser.id}`;

/** 展示和复制均使用后台解密后的完整号码。 */
export function ChargeUserPhone({ chargeUser }: { chargeUser: Pick<ChargeUser, 'phone' | 'phone_bound' | 'id'> }) {
  if (!chargeUser.phone_bound) return <Typography.Text type="secondary">未绑定</Typography.Text>;
  return <Typography.Text copyable={{ text: chargeUser.phone }} style={{ fontVariantNumeric: 'tabular-nums' }}>{chargeUser.phone}</Typography.Text>;
}

type ProfileState = { userID: number | null; detail: ChargeUserDetail | null; error: string; loading: boolean };

export default function ChargeUserProfileDrawer({ userID, onClose }: { userID: number | null; onClose: () => void }) {
  const [state, setState] = useState<ProfileState>({ userID: null, detail: null, error: '', loading: false });
  const [reload, setReload] = useState(0);
  const closeRef = useRef(onClose);
  closeRef.current = onClose;

  useEffect(() => {
    if (userID == null) {
      setState({ userID: null, detail: null, error: '', loading: false });
      return;
    }
    const controller = new AbortController();
    const epoch = adminSession.epoch();
    const current = () => !controller.signal.aborted && epoch === adminSession.epoch();
    const closeExpired = () => {
      if (epoch !== adminSession.epoch()) {
        controller.abort();
        setState({ userID: null, detail: null, error: '', loading: false });
        closeRef.current();
      }
    };
    window.addEventListener('cp-session', closeExpired);
    window.addEventListener('storage', closeExpired);
    setState({ userID, detail: null, error: '', loading: true });
    http.get<ApiEnvelope<ChargeUserDetail>>(`/api/v1/admin/charge-users/${userID}`, { signal: controller.signal })
      .then(response => {
        if (!current()) return;
        const detail = response.data.data;
        if (!detail || detail.id !== userID) throw new Error('用户档案响应不完整，请重新读取');
        setState({ userID, detail, error: '', loading: false });
      })
      .catch(cause => {
        if (current()) setState({ userID, detail: null, error: cause instanceof Error ? cause.message : '加载失败，请稍后重试', loading: false });
      });
    return () => {
      controller.abort();
      window.removeEventListener('cp-session', closeExpired);
      window.removeEventListener('storage', closeExpired);
    };
  }, [userID, reload]);

  const visible = state.userID === userID ? state : { detail: null, error: '', loading: userID != null };
  const detail = visible.detail;
  return <Drawer title={detail ? chargeUserName(detail) : '充电用户档案'} open={userID != null} onClose={onClose} width="min(760px, 100vw)">
    {visible.loading && <Spin />}
    {visible.error && <LoadError title="档案加载失败" detail={visible.error} onRetry={() => setReload(value => value + 1)} />}
    {detail && <Space direction="vertical" size="large" style={{ width: '100%' }}>
      <Descriptions title="账号" bordered column={2} items={[
        { key: 'id', label: '用户 ID', children: detail.id },
        { key: 'status', label: '状态', children: chargeUserStatusTag(detail.status) },
        { key: 'phone', label: '手机号', children: <ChargeUserPhone chargeUser={detail} />, span: 2 },
        { key: 'openid', label: 'openid', children: detail.openid, span: 2 },
        { key: 'unionid', label: 'unionid', children: detail.union_id || '—' },
        { key: 'gender', label: '性别', children: genders[detail.gender] || detail.gender },
        { key: 'first', label: '首次出现', children: chargeUserTime(detail.first_seen_at) },
        { key: 'last', label: '最后登录', children: chargeUserTime(detail.last_login_at) },
        { key: 'inviter', label: '邀请人', children: detail.inviter_id ? `#${detail.inviter_id}` : '自然注册' },
      ]} />
      <Descriptions title="钱包与消费" bordered column={2} items={[
        { key: 'balance', label: '可用余额', children: chargeUserMoney(detail.balance_cents) },
        { key: 'wallet', label: '钱包状态', children: walletStatuses[detail.wallet_status] || detail.wallet_status },
        { key: 'frozen', label: '冻结金额', children: chargeUserMoney(detail.frozen_cents) },
        { key: 'lastorder', label: '最近下单', children: chargeUserTime(detail.last_order_at) },
        { key: 'orders', label: '历史订单', children: `${detail.order_count} 笔` },
        { key: 'total', label: '累计消费', children: chargeUserMoney(detail.total_cents) },
        { key: 'coupon', label: '优惠券', children: `累计发放 ${detail.coupon_granted} 张，未使用 ${detail.coupon_unused} 张` },
        { key: 'fault', label: '报障单', children: `${detail.fault_reports} 单` },
      ]} />
      <Typography.Title level={5}>最近订单</Typography.Title>
      {!detail.recent_orders.length && <Typography.Text type="secondary">该用户暂无充电订单</Typography.Text>}
      <Table<ChargeUserOrder> rowKey="order_id" size="middle" pagination={TABLE_PAGINATION} dataSource={detail.recent_orders}
        locale={{ emptyText: '暂无订单' }} columns={[
          { title: '订单号', dataIndex: 'order_no' },
          { title: '设备', dataIndex: 'device_id' },
          { title: '业务状态', key: 'business_status', render: (_, row) => orderTag(row, 'business') },
          { title: '支付状态', key: 'payment_status', render: (_, row) => orderTag(row, 'payment') },
          { title: '金额', dataIndex: 'total_cents', align: 'right', render: value => value == null ? '待结算' : chargeUserMoney(value) },
          { title: '下单时间', dataIndex: 'created_at', render: chargeUserTime },
        ]} />
    </Space>}
  </Drawer>;
}
