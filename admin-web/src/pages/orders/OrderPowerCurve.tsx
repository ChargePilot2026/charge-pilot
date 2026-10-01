import { Descriptions, Empty, Slider, Space, Spin, Tooltip, Typography } from 'antd';
import axios from 'axios';
import dayjs from 'dayjs';
import { useEffect, useMemo, useState } from 'react';
import { type ApiEnvelope, http } from '../../api/client';
import { LoadError } from '../../components/LoadError';
import { formatDuration } from './presentation';
import { nearestPowerPoint, powerAxisStep, powerPoints, powerSegments, readProcessPages, type ProcessPage, type ProcessSample } from './powerCurve';

const sampleTime = (value: string) => dayjs(value).format('YYYY-MM-DD HH:mm:ss.SSS');
const energy = (value: number) => Number.isFinite(value) ? `${value.toFixed(3)} kWh` : '—';
const errorMessage = (cause: unknown) => axios.isAxiosError<ApiEnvelope>(cause)
  ? cause.response?.data?.message || cause.message
  : cause instanceof Error ? cause.message : '功率采样读取失败，请稍后重试';

function SampleTooltip({ sample }: { sample: ProcessSample }) {
  return <div>
    <div>{sampleTime(sample.ts)}</div>
    <div>功率：{sample.power_w} W</div>
    <div>累计电量：{energy(sample.charged_kwh)}</div>
    <div>充电时长：{formatDuration(sample.charged_seconds)}</div>
  </div>;
}

export default function OrderPowerCurve({ orderID, active, charging }: {
  orderID: number; active: boolean; charging: boolean;
}) {
  const [samples, setSamples] = useState<ProcessSample[]>([]);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState('');
  const [reload, setReload] = useState(0);
  const [selectedID, setSelectedID] = useState<number>();
  const [hovering, setHovering] = useState(false);
  const points = useMemo(() => powerPoints(samples), [samples]);
  const segments = useMemo(() => powerSegments(points), [points]);

  useEffect(() => { setSamples([]); setSelectedID(undefined); setHovering(false); setError(''); }, [orderID]);

  useEffect(() => {
    if (!active) { setLoading(false); setHovering(false); return; }
    let disposed = false, running = false, lastID: number | undefined;
    let collected: ProcessSample[] = [];
    let controller: AbortController | undefined;
    const load = async () => {
      if (running) return;
      running = true;
      controller = new AbortController();
      setLoading(true); setError('');
      try {
        const incoming = await readProcessPages(async (afterID, signal) => {
          const result = await http.get<ApiEnvelope<ProcessPage>>(`/api/v1/admin/orders/${orderID}/process`, {
            signal, params: { after_id: afterID, limit: 1000 },
          });
          if (!result.data.data) throw new Error('功率采样响应为空，请重试');
          return result.data.data;
        }, controller.signal, lastID);
        const next = [...collected, ...incoming];
        powerPoints(next);
        if (disposed) return;
        collected = next;
        if (incoming.length) lastID = incoming.reduce((maximum, sample) => Math.max(maximum, sample.id), lastID ?? 0);
        setSamples(next);
      } catch (cause: unknown) {
        if (!disposed && !controller.signal.aborted) setError(errorMessage(cause));
      } finally {
        running = false;
        if (!disposed) setLoading(false);
      }
    };
    void load();
    const timer = charging ? window.setInterval(() => void load(), 15_000) : undefined;
    return () => { disposed = true; controller?.abort(); if (timer) window.clearInterval(timer); };
  }, [orderID, active, charging, reload]);

  const selectedIndex = selectedID == null ? points.length - 1 : points.findIndex(point => point.sample.id === selectedID);
  const index = selectedIndex < 0 ? points.length - 1 : selectedIndex;
  const selected = points[index];
  const first = points[0]?.at ?? 0, last = points[points.length - 1]?.at ?? first;
  const start = first === last ? first - 30_000 : first, end = first === last ? last + 30_000 : last;
  const step = powerAxisStep(points.reduce((peak, point) => Math.max(peak, point.sample.power_w), 0));
  const left = 72, top = 28, bottom = 262, width = 680;
  const x = (at: number) => left + (at - start) / (end - start) * width;
  const y = (watts: number) => bottom - watts / (step * 4) * (bottom - top);

  return <Space direction="vertical" size="middle" style={{ width: '100%' }}>
    {error && <LoadError title="功率曲线加载失败" detail={error} onRetry={() => setReload(value => value + 1)} />}
    {loading && !points.length && <div style={{ padding: 32, textAlign: 'center' }}><Spin /></div>}
    {!loading && !error && !points.length && <Empty description="暂无功率采样记录" />}
    {points.length > 0 && <>
      <Space wrap>
        <Typography.Text type="secondary">共 {points.length} 个采样，超过 60 秒的采样间隔不连接。</Typography.Text>
        {loading && <Spin size="small" />}
      </Space>
      <div style={{ width: '100%', overflowX: 'auto' }}>
        <Tooltip open={active && hovering && Boolean(selected)} title={selected ? <SampleTooltip sample={selected.sample} /> : null} placement="top">
          <svg viewBox="0 0 800 330" role="img" aria-label="订单真实心跳功率曲线，横轴采样时间，纵轴功率瓦"
            style={{ display: 'block', width: '100%', minWidth: 480 }}
            onMouseEnter={() => setHovering(true)} onMouseLeave={() => setHovering(false)}
            onMouseMove={event => {
              const rect = event.currentTarget.getBoundingClientRect();
              const svgX = (event.clientX - rect.left) / rect.width * 800;
              const at = start + Math.max(0, Math.min(1, (svgX - left) / width)) * (end - start);
              const nearest = nearestPowerPoint(points, at);
              if (nearest >= 0) setSelectedID(points[nearest].sample.id);
            }}>
            <title>订单功率曲线</title>
            <desc>{`共有 ${points.length} 个真实心跳采样，${sampleTime(points[0].sample.ts)} 至 ${sampleTime(points[points.length - 1].sample.ts)}。超过60秒处断开曲线。使用下方采样滑块和方向键逐个查看时间、功率、累计电量与时长。`}</desc>
            <text x={left - 12} y={16} textAnchor="end" fill="#8c8c8c" fontSize={12}>功率（W）</text>
            {[0, 1, 2, 3, 4].map(tick => {
              const ordinate = y(tick * step);
              return <g key={tick}>
                <line x1={left} x2={left + width} y1={ordinate} y2={ordinate} stroke="#f0f0f0" />
                <text x={left - 10} y={ordinate + 4} textAnchor="end" fill="#8c8c8c" fontSize={12}>{Number((tick * step).toPrecision(6))}</text>
              </g>;
            })}
            {segments.map((segment, position) => segment.length === 1
              ? <circle key={position} cx={x(segment[0].at)} cy={y(segment[0].sample.power_w)} r={3} fill="#1677ff" />
              : <polyline key={position} fill="none" stroke="#1677ff" strokeWidth={2} strokeLinejoin="round"
                points={segment.map(point => `${x(point.at)},${y(point.sample.power_w)}`).join(' ')} />)}
            {[0, 1, 2, 3, 4].map(tick => {
              const at = start + tick / 4 * (end - start);
              return <text key={tick} x={x(at)} y={bottom + 22} textAnchor={tick === 0 ? 'start' : tick === 4 ? 'end' : 'middle'} fill="#8c8c8c" fontSize={12}>
                {dayjs(at).format(end - start > 86_400_000 ? 'MM-DD HH:mm' : 'HH:mm:ss')}
              </text>;
            })}
            <text x={left + width / 2} y={bottom + 51} textAnchor="middle" fill="#8c8c8c" fontSize={12}>采样时间</text>
            {selected && <g>
              <line x1={x(selected.at)} x2={x(selected.at)} y1={top} y2={bottom} stroke="#91caff" strokeDasharray="4 4" />
              <circle cx={x(selected.at)} cy={y(selected.sample.power_w)} r={4} fill="#fff" stroke="#1677ff" strokeWidth={2}>
                <title>{`${sampleTime(selected.sample.ts)}：${selected.sample.power_w} W`}</title>
              </circle>
            </g>}
          </svg>
        </Tooltip>
      </div>
      <Slider min={0} max={Math.max(0, points.length - 1)} value={index} disabled={points.length === 1}
        ariaLabelForHandle="选择功率采样" ariaValueTextFormatterForHandle={value => value == null ? '' : `${sampleTime(points[value].sample.ts)}，${points[value].sample.power_w} W`}
        onChange={next => setSelectedID(points[next].sample.id)}
        tooltip={{ formatter: value => value == null ? null : <SampleTooltip sample={points[value].sample} /> }} />
      {selected && <Descriptions size="small" column={{ xs: 1, sm: 2 }} items={[
        { key: 'sampled', label: '采样时间', children: sampleTime(selected.sample.ts) },
        { key: 'power', label: '功率', children: `${selected.sample.power_w} W` },
        { key: 'charged', label: '累计电量', children: energy(selected.sample.charged_kwh) },
        { key: 'seconds', label: '充电时长', children: formatDuration(selected.sample.charged_seconds) },
      ]} />}
    </>}
  </Space>;
}
