import { Alert, Button } from 'antd';
import type { CSSProperties } from 'react';

/**
 * LoadError 是「这一屏的数据没拿到」的唯一表达方式。
 *
 * 之前同一个意思有三套写法：antd Alert（有的带图标有的不带）、toast 冒一下就
 * 没了、以及一行红色的裸文字。它们给运营的感觉完全不一样——toast 三秒后画面
 * 恢复原样，看不出到底哪块数据没加载；裸文字则和正文混在一起，排版上像一句提示
 * 而不是一次故障。列表空白到底是"没有数据"还是"没加载出来"更无从判断。
 *
 * 所以统一成一条：红色 × 图标 + 一句话说清是哪个功能坏了 + 后端给的原始原因 +
 * 一个重试按钮。标题必须点名具体功能（"订单加载失败"），因为后台一屏常常同时有
 * 好几块数据，出错时运营第一眼要靠标题知道该找谁。
 *
 * 只用于**读取/加载**失败。提交、保存、审核这类写操作的失败不要用这个：表单就在
 * 用户眼前，toast 或字段级报错才是对的，一屏大 Alert 会把表单挤下去。
 */
export function LoadError({ title, detail, onRetry, style }: {
  /** 点名是哪个功能读不到数据，例如「订单加载失败」。 */
  title: string;
  /** 后端返回的原始原因，直接透出，不另写一套说辞。 */
  detail?: string;
  /** 传入则显示重试按钮；没有可重试的入口时省略。 */
  onRetry?: () => void;
  style?: CSSProperties;
}) {
  return (
    <Alert
      type="error"
      showIcon
      message={title}
      description={detail || undefined}
      style={{ marginBottom: 16, ...style }}
      action={onRetry ? <Button size="small" onClick={onRetry}>重试</Button> : undefined}
    />
  );
}
