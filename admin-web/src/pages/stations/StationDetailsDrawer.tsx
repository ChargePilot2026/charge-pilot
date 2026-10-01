import { Drawer } from 'antd';
import { useEffect, useRef } from 'react';
import { adminSession } from '../../api/client';
import StationWorkspace, { type StationRecord } from './StationWorkspace';

export type StationReference = { id: number; name?: string | null };

export default function StationDetailsDrawer({ station, permissions, initialDeviceId = null, onClose, onSaved }: {
  station: StationReference | null;
  permissions: string[];
  initialDeviceId?: string | null;
  onClose: () => void;
  onSaved?: (station: StationRecord) => void;
}) {
  const closeRef = useRef(onClose);
  closeRef.current = onClose;
  useEffect(() => {
    if (!station) return;
    const session = adminSession.epoch();
    const closeExpired = () => { if (session !== adminSession.epoch()) closeRef.current(); };
    window.addEventListener('cp-session', closeExpired);
    window.addEventListener('storage', closeExpired);
    return () => {
      window.removeEventListener('cp-session', closeExpired);
      window.removeEventListener('storage', closeExpired);
    };
  }, [station?.id]);

  return <Drawer title={station?.name ? `${station.name} · 站点管理` : '站点管理'} open={station != null}
    width="min(1120px, 100vw)" destroyOnHidden onClose={onClose}>
    {station && <StationWorkspace key={station.id} station={station} permissions={permissions} initialDeviceId={initialDeviceId}
      onSaved={updated => onSaved?.(updated)} />}
  </Drawer>;
}
