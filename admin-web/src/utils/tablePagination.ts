import type { TablePaginationConfig } from 'antd';
import { useState } from 'react';

export const DEFAULT_PAGE_SIZE = 10;

export const TABLE_PAGINATION: TablePaginationConfig = {
  defaultPageSize: DEFAULT_PAGE_SIZE,
  showSizeChanger: true,
  pageSizeOptions: [10, 20, 50, 100],
  position: ['topRight', 'bottomRight'],
};

export function useTablePagination() {
  const [pagination, setPagination] = useState({ page: 1, page_size: DEFAULT_PAGE_SIZE });
  const tablePagination: TablePaginationConfig = {
    ...TABLE_PAGINATION,
    current: pagination.page,
    pageSize: pagination.page_size,
    onChange: (page, pageSize) => setPagination(current => ({
      page: pageSize === current.page_size ? page : 1,
      page_size: pageSize,
    })),
  };
  return { pagination, tablePagination, setPagination };
}
