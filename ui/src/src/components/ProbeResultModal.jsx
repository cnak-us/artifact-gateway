import { useMemo, useState } from 'react';
import { MdCheck, MdContentCopy } from 'react-icons/md';
import clsx from 'clsx';
import Modal from './Modal.jsx';
import Button from './Button.jsx';
import Spinner from './Spinner.jsx';
import Badge from './Badge.jsx';
import ErrorBanner from './ErrorBanner.jsx';

// Reusable modal for rendering a probe/test result from the backend.
// Both /upstream-credentials/{id}/test and /packages/{id}/probe return the same
// shape (see the response contract in the task brief), so this component has no
// special-casing for endpoint kind — the backend's `summary` carries the headline.
export default function ProbeResultModal({
  open,
  onClose,
  title,
  loading = false,
  result = null,
  error = null,
}) {
  return (
    <Modal
      open={open}
      onClose={onClose}
      title={title}
      size="lg"
      footer={<Button variant="secondary" onClick={onClose}>Close</Button>}
    >
      <div className="space-y-4">
        {!loading && !error && result && result.multi_container ? (
          <MultiContainerResult containers={result.containers} />
        ) : (
          <>
            <StatusBanner loading={loading} result={result} error={error} />

            {!loading && !error && result && (
              <>
                <RequestLine result={result} />
                <Summary text={result.summary} />
                <HeadersSection headers={result.headers} />
                <BodySection body={result.body} />
              </>
            )}
          </>
        )}
      </div>
    </Modal>
  );
}

function MultiContainerResult({ containers }) {
  const rows = Array.isArray(containers) ? containers : [];
  if (rows.length === 0) {
    return (
      <p className="text-sm text-g-text-secondary italic">
        Multi-container package with no containers configured. Add at least one container row to probe it.
      </p>
    );
  }
  return (
    <div className="space-y-4">
      {rows.map((c) => {
        const ok = !!(c.result && c.result.ok);
        const statusText = c.result?.status_text || (c.result?.status ? String(c.result.status) : '—');
        const latency = typeof c.result?.duration_ms === 'number' ? `${c.result.duration_ms} ms` : null;
        return (
          <section key={c.alias} className="border border-g-border-weak rounded p-3 space-y-2">
            <div className="flex items-center justify-between gap-3">
              <div className="flex items-center gap-2">
                <Badge color={ok ? 'green' : 'red'} className="text-xs px-2 py-0.5">{statusText}</Badge>
                <span className="font-mono text-sm">{c.alias}</span>
                {c.display_name && (
                  <span className="text-xs text-g-text-secondary">{c.display_name}</span>
                )}
              </div>
              {latency && <span className="text-xs text-g-text-secondary font-mono">({latency})</span>}
            </div>
            <RequestLine result={c.result || {}} />
            <Summary text={c.result?.summary} />
          </section>
        );
      })}
    </div>
  );
}

function StatusBanner({ loading, result, error }) {
  if (loading) {
    return (
      <div className="flex items-center justify-center py-6">
        <Spinner label="Calling upstream…" />
      </div>
    );
  }
  if (error) {
    return <ErrorBanner error={error} />;
  }
  if (!result) return null;

  const ok = !!result.ok;
  const statusText = result.status_text || (result.status ? String(result.status) : '—');
  const latency = typeof result.duration_ms === 'number' ? `${result.duration_ms} ms` : null;

  return (
    <div className="flex items-center justify-between gap-3">
      <Badge color={ok ? 'green' : 'red'} className="text-sm px-2 py-1">{statusText}</Badge>
      {latency && <span className="text-xs text-g-text-secondary font-mono">({latency})</span>}
    </div>
  );
}

function RequestLine({ result }) {
  if (!result.url && !result.method) return null;
  return (
    <div className="font-mono text-xs text-g-text-secondary break-all">
      <span className="text-g-text">{result.method || 'GET'}</span>
      {' '}
      {result.url}
    </div>
  );
}

function Summary({ text }) {
  if (!text) return null;
  return <p className="text-sm text-g-text leading-relaxed">{text}</p>;
}

function HeadersSection({ headers }) {
  const entries = headers && typeof headers === 'object' ? Object.entries(headers) : [];
  if (entries.length === 0) return null;
  return (
    <section>
      <h3 className="text-xs uppercase tracking-wider font-medium text-g-text-disabled mb-2">Headers</h3>
      <dl className="grid grid-cols-[max-content_1fr] gap-x-4 gap-y-1 border border-g-border-weak rounded bg-g-secondary/40 px-3 py-2">
        {entries.map(([k, v]) => (
          <FragmentRow key={k} k={k} v={v} />
        ))}
      </dl>
    </section>
  );
}

function FragmentRow({ k, v }) {
  return (
    <>
      <dt className="font-mono text-xs text-g-text-secondary">{k}</dt>
      <dd className="font-mono text-xs text-g-text break-all">{String(v)}</dd>
    </>
  );
}

function BodySection({ body }) {
  const [view, setView] = useState('pretty');
  const [copied, setCopied] = useState(false);

  const text = useMemo(() => {
    if (body == null) return '';
    if (typeof body === 'string') return body;
    try { return JSON.stringify(body, null, 2); }
    catch { return String(body); }
  }, [body]);

  const onCopy = async () => {
    try {
      await navigator.clipboard.writeText(text);
      setCopied(true);
      setTimeout(() => setCopied(false), 1500);
    } catch {/* clipboard blocked */}
  };

  return (
    <section>
      <div className="flex items-center justify-between mb-2">
        <h3 className="text-xs uppercase tracking-wider font-medium text-g-text-disabled">Response body</h3>
        <div className="flex items-center gap-2">
          <TabSwitch
            value={view}
            onChange={setView}
            options={[
              { value: 'pretty', label: 'Pretty' },
              { value: 'raw',    label: 'Raw' },
            ]}
          />
          <button
            type="button"
            onClick={onCopy}
            title={copied ? 'Copied!' : 'Copy'}
            className={clsx(
              'inline-flex items-center gap-1 px-2 py-1 rounded text-[11px] font-medium',
              'border border-g-border-weak text-g-text-secondary hover:text-g-text hover:bg-g-hover transition-colors',
              copied && 'text-g-green-text border-g-green-main/30',
            )}
          >
            {copied ? <MdCheck className="text-g-green-text" /> : <MdContentCopy />}
            {copied ? 'Copied' : 'Copy'}
          </button>
        </div>
      </div>
      <pre className={clsx(
        'm-0 p-3 text-xs text-g-text font-mono leading-relaxed',
        'border border-g-border-weak rounded bg-g-canvas',
        'max-h-[40vh] overflow-y-auto',
        view === 'pretty' ? 'whitespace-pre-wrap break-words' : 'whitespace-pre overflow-x-auto',
      )}>{text || <span className="text-g-text-disabled italic">(empty body)</span>}</pre>
    </section>
  );
}

function TabSwitch({ value, onChange, options }) {
  return (
    <div
      role="tablist"
      className="inline-flex p-0.5 rounded border border-g-border-weak bg-g-secondary/60"
    >
      {options.map((opt) => {
        const active = opt.value === value;
        return (
          <button
            key={opt.value}
            type="button"
            role="tab"
            aria-selected={active}
            onClick={() => onChange(opt.value)}
            className={clsx(
              'px-2.5 py-1 rounded text-[11px] font-medium transition-colors',
              active
                ? 'bg-g-elevated text-g-text shadow-z1'
                : 'text-g-text-secondary hover:text-g-text',
            )}
          >
            {opt.label}
          </button>
        );
      })}
    </div>
  );
}
