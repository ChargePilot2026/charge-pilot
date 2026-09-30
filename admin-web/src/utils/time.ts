import dayjs from 'dayjs';

/**
 * formatTime 把后端给的 RFC3339 时间渲染成本地时区的固定格式。
 *
 * 后端统一以 UTC 存储、按 UTC 返回（连接层 config.Loc = time.UTC），前端一律
 * 在这里换算成本地时区。表格列漏写 render 时 Ant Design 会把原始串直接印出来，
 * 界面上就是 2026-09-30T19：54：11.358Z 这种东西——运维对着 UTC 时间排查问题，
 * 和用户报障时对不上。所以时间列一律走这个函数，不要直接把 dataIndex 扔给表格。
 */
export function formatTime(value: string | null | undefined): string {
  return value ? dayjs(value).format('YYYY-MM-DD HH:mm:ss') : '—';
}

/** formatMinute 精度到分，用于列表里"最近登录""最近下单"这类只需看分钟的列。 */
export function formatMinute(value: string | null | undefined): string {
  return value ? dayjs(value).format('YYYY-MM-DD HH:mm') : '—';
}
