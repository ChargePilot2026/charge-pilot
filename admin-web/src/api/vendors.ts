import { apiGet } from './client';

export type Vendor = {
  id: number;
  vendor_code: string;
  vendor_name: string;
  adapter_class: string;
  protocol: string;
  status: 'enabled' | 'disabled';
};

export type VendorPage = { items: Vendor[]; total: number; page: number; page_size: number; permissions?: string[] };

export const listVendors = (query: Record<string, unknown>, optionsOnly = false) =>
  apiGet<VendorPage>(optionsOnly ? '/api/v1/admin/vendor-options' : '/api/v1/admin/vendors', query);

export const vendorProtocolLabel = (vendor: Pick<Vendor, 'adapter_class' | 'protocol'>) =>
  vendor.adapter_class === 'dc589' && vendor.protocol === 'tcp'
    ? 'DC589 · TCP' : `${vendor.adapter_class} · ${vendor.protocol.toUpperCase()}`;
