import { useEffect, useState } from 'react';
import { MdChevronRight, MdRefresh, MdListAlt } from 'react-icons/md';
import { admin } from '../../api/client.js';
import Button from '../../components/Button.jsx';
import Spinner from '../../components/Spinner.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import Table from '../../components/Table.jsx';
import Badge from '../../components/Badge.jsx';
import EmptyState from '../../components/EmptyState.jsx';

// PAGE must match (or stay <=) the server's hardcoded cap in
// server/admin.go:listAuditEvents (currently 100). When the server returns
// exactly PAGE rows we assume there's another page and offer a Next button;
// when it returns fewer we know we've hit the end.
const PAGE = 50;

export default function Audit() {
  const [items, setItems] = useState(null);
  const [before, setBefore] = useState(null);   // current page's `before` cursor (older-than)
  const [history, setHistory] = useState([]);   // stack of previous `before` values for Back
  const [err, setErr] = useState(null);
  const [loading, setLoading] = useState(false);

  // Server returns a flat array (oldest events come back at the tail because
  // it orders DESC). We derive the next cursor as the oldest row's timestamp.
  const load = async (cursor) => {
    setLoading(true);
    setErr(null);
    try {
      const res = await admin.auditEvents({ limit: PAGE, before: cursor || undefined });
      const list = Array.isArray(res) ? res : (res?.items || []);
      setItems(list);
    } catch (e) { setErr(e); }
    finally { setLoading(false); }
  };

  useEffect(() => { load(null); }, []);

  const hasNextPage = items && items.length >= PAGE;
  const oldestTs = items && items.length > 0 ? items[items.length - 1].timestamp : null;

  const next = () => {
    if (!hasNextPage || !oldestTs) return;
    setHistory((h) => [...h, before]);
    setBefore(oldestTs);
    load(oldestTs);
  };
  const prev = () => {
    if (history.length === 0) return;
    const prevCursor = history[history.length - 1];
    setHistory((h) => h.slice(0, -1));
    setBefore(prevCursor);
    load(prevCursor);
  };
  const refresh = () => { setHistory([]); setBefore(null); load(null); };

  // Column renderers read the actual server JSON shape (audit.AuditEvent):
  // timestamp, userId, username, action, resourceType, resourceId,
  // resourceName, details, ipAddress, status, errorMessage, source.
  const columns = [
    {
      key: 'timestamp',
      header: 'Time',
      render: (e) => (
        <span className="text-xs whitespace-nowrap">
          {e.timestamp ? new Date(e.timestamp).toLocaleString() : '—'}
        </span>
      ),
    },
    {
      key: 'actor',
      header: 'Actor',
      render: (e) => (
        <div className="text-xs">
          <div>{e.username || (e.userId ? e.userId : 'system')}</div>
          {e.ipAddress && <div className="text-g-text-secondary font-mono">{e.ipAddress}</div>}
        </div>
      ),
    },
    { key: 'action', header: 'Action', render: (e) => <Badge>{e.action || '—'}</Badge> },
    {
      key: 'resource',
      header: 'Resource',
      render: (e) => (
        <div className="text-xs">
          <div className="font-mono">{e.resourceType || '—'}</div>
          {e.resourceName && <div>{e.resourceName}</div>}
          {e.resourceId && <div className="text-g-text-secondary font-mono text-[10px]">{e.resourceId}</div>}
        </div>
      ),
    },
    {
      key: 'status',
      header: 'Status',
      render: (e) => {
        const s = e.status || 'success';
        const ok = s === 'success';
        return <Badge color={ok ? 'green' : 'red'}>{s}</Badge>;
      },
    },
    {
      key: 'details',
      header: 'Details',
      render: (e) => (
        <div className="text-xs max-w-md">
          {e.details && <div className="text-g-text-secondary break-words">{e.details}</div>}
          {e.errorMessage && <div className="text-g-red-text break-words">{e.errorMessage}</div>}
        </div>
      ),
    },
  ];

  return (
    <div className="space-y-4">
      <div className="flex items-center justify-between gap-3 flex-wrap">
        <div>
          <h1 className="text-xl font-semibold">Audit log</h1>
          <p className="text-sm text-g-text-secondary max-w-2xl">
            All admin and customer actions, also published to NATS.
          </p>
        </div>
        <Button variant="outline" icon={<MdRefresh />} onClick={refresh}>Refresh</Button>
      </div>

      <ErrorBanner error={err} />

      {items === null ? <Spinner label="Loading events" /> : items.length === 0 ? (
        <EmptyState icon={MdListAlt} title="No audit events" description="Activity will appear here as admins and customers act on the gateway." />
      ) : (
        <Table
          columns={columns}
          rows={items}
          rowKey={(r) => r.id || `${r.timestamp}-${r.action}-${r.resourceId || ''}`}
        />
      )}

      <div className="flex justify-between items-center text-sm">
        <div className="text-g-text-secondary">
          {items ? `${items.length} event${items.length === 1 ? '' : 's'}` : ''}
          {loading && ' · loading…'}
        </div>
        <div className="flex gap-2">
          <Button variant="outline" onClick={prev} disabled={history.length === 0 || loading}>Previous</Button>
          <Button variant="outline" iconRight={<MdChevronRight />} onClick={next} disabled={!hasNextPage || loading}>Next</Button>
        </div>
      </div>
    </div>
  );
}
