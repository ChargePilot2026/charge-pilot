export function apiErrorMessage(status: number | undefined, data: unknown, code?: string): string {
  if (!status) return code === 'ECONNABORTED' || code === 'ETIMEDOUT' ? '请求超时，请稍后重试；提交结果请刷新核实' : '无法连接服务，请检查网络后重试';
  if (status === 401) return '登录已失效，请重新登录';
  if (status >= 500) return '服务暂时不可用，请稍后重试';
  if (data && typeof data === 'object' && 'message' in data && typeof data.message === 'string' && data.message.trim()) return data.message;
  if (status === 403) return '没有操作权限，请联系管理员';
  if (status === 404) return '记录不存在或已被删除，请刷新列表';
  if (status === 409) return '记录状态已变化，请刷新后重试';
  if (status === 429) return '操作过于频繁，请稍后重试';
  return '请求未成功，请检查输入后重试';
}
