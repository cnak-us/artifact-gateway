import { useCallback, useEffect, useMemo, useState } from 'react';
import { MdDownload, MdKey, MdRefresh, MdAdd } from 'react-icons/md';
import { useCatalogAuth } from '../../contexts/CatalogAuthContext.jsx';
import { catalog } from '../../api/client.js';
import CopyableCode from '../../components/CopyableCode.jsx';
import Spinner from '../../components/Spinner.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';
import Modal from '../../components/Modal.jsx';
import Button from '../../components/Button.jsx';
import { useConfirm } from '../../components/ConfirmDialog.jsx';

// CatalogCredentials renders one card per license the session can act on
// (session.licenses[]). Each card has its own "get / rotate" state machine —
// rotating one license doesn't affect the others. The plaintext secret is
// shown once in a modal; refreshing the page returns to the metadata-only
// view because the secret only lives in component memory.
export default function CatalogCredentials() {
  const { session } = useCatalogAuth();
  const [blob, setBlob] = useState(null);
  const [blobErr, setBlobErr] = useState(null);

  useEffect(() => {
    if (!session) return;
    let cancelled = false;
    catalog.getLicenseBlob()
      .then((t) => { if (!cancelled) setBlob(t); })
      .catch((e) => { if (!cancelled) setBlobErr(e); });
    return () => { cancelled = true; };
  }, [session]);

  const licenses = useMemo(() => {
    // Backend exposes session.licenses[] for multi-license users; fall back
    // to a single synthetic entry when the (older / single-license) payload
    // only carries top-level fields. This keeps the UI compatible with both.
    if (Array.isArray(session?.licenses) && session.licenses.length > 0) {
      return session.licenses;
    }
    if (session?.license_id) {
      return [{
        id: session.license_id, // best-effort; backend may also send a UUID via session.license.id
        license_id: session.license_id,
        customer: session.customer,
        organization: session.organization,
        tier: session.tier,
        expires_at: session.expires_at,
      }];
    }
    return [];
  }, [session]);

  if (!session) return null;

  return (
    <div className="max-w-3xl mx-auto px-6 py-10">
      <header className="mb-8">
        <h1 className="text-2xl font-semibold tracking-tight">My credential</h1>
        <p className="mt-2 text-sm text-g-text-secondary max-w-xl">
          One docker-pull credential per license. Rotating a credential
          immediately revokes the previous one — pulls using the old secret
          start failing within seconds.
        </p>
      </header>

      {licenses.length > 1 && (
        <div className="mb-6 px-4 py-3 rounded bg-g-blue-main/10 border border-g-blue-main/30 text-sm text-g-text">
          You have credentials for <strong>{licenses.length}</strong> licenses.
          Each is independent — rotate them separately.
        </div>
      )}

      <div className="space-y-6">
        {licenses.map((lic) => (
          <LicenseCredentialCard key={lic.id || lic.license_id} license={lic} />
        ))}
        {licenses.length === 0 && (
          <p className="text-sm text-g-text-secondary">
            No license is associated with this session.
          </p>
        )}
      </div>

      {/* License file — raw .lic blob. Unchanged from prior version; useful
          for offline import into the CNAK admin UI. */}
      <section className="bg-g-elevated border border-g-border-weak rounded mt-8">
        <div className="px-5 py-4 border-b border-g-border-weak flex items-center justify-between gap-2 flex-wrap">
          <div>
            <h2 className="text-sm font-semibold">License file</h2>
            <p className="text-xs text-g-text-secondary mt-0.5">
              Signed by the gateway's active root key. Paste into the CNAK admin UI
              or save as <code className="text-[11px]">.lic</code>.
            </p>
          </div>
          <a
            href={catalog.downloadLicense()}
            download
            className="inline-flex items-center gap-1.5 px-2.5 py-1.5 rounded text-xs font-medium bg-g-elevated border border-g-border-medium text-g-text hover:bg-g-hover transition-colors"
          >
            <MdDownload /> Download .lic
          </a>
        </div>
        <div className="p-4">
          <ErrorBanner error={blobErr} />
          {blob === null && !blobErr ? (
            <Spinner label="Loading license blob" />
          ) : blob !== null ? (
            <CopyableCode value={blob} language="lic" />
          ) : null}
        </div>
      </section>
    </div>
  );
}

// LicenseCredentialCard owns one license's credential state. Status:
//   loading   — initial fetch
//   none      — no active credential; "Create credential" button
//   active    — show metadata + "Rotate credential" button
//   error     — fetch/rotate failed; "Try again" button
// After a successful create/rotate the plaintext secret is shown in a Modal;
// dismissing the modal refetches metadata so the card returns to `active`.
// Shown as the disabled-button tooltip and as the secondary hint underneath
// the button when the admin has turned off customer self-rotation for this
// license. We intentionally keep the button visible (just disabled) so the
// affordance is discoverable — see the team design note.
const ROTATE_DISABLED_HINT =
  'Credential rotation is disabled by admin. Contact support if you need a new credential.';

function LicenseCredentialCard({ license }) {
  const confirm = useConfirm();
  const [status, setStatus] = useState('loading');
  const [data, setData] = useState(null);
  const [err, setErr] = useState(null);
  const [pending, setPending] = useState(false);
  const [revealed, setRevealed] = useState(null); // { token_id, secret, full_credential }
  // Default to true when the field is absent (older server, transient race)
  // so we never lock the user out due to a missing flag.
  const customerRotateEnabled = data?.customer_rotate_enabled !== false;

  const refresh = useCallback(async () => {
    setStatus('loading');
    setErr(null);
    try {
      const res = await catalog.getCredential(license.id);
      if (!res || !res.token_id) {
        // Preserve the "no active token" payload (it still carries
        // customer_rotate_enabled) so the disabled-button hint can render
        // in the none-state too.
        setData(res || null);
        setStatus('none');
      } else {
        setData(res);
        setStatus('active');
      }
    } catch (e) {
      setErr(e);
      setStatus('error');
    }
  }, [license.id]);

  useEffect(() => { void refresh(); }, [refresh]);

  const rotate = async () => {
    if (status === 'active') {
      const ok = await confirm({
        title: 'Rotate credential?',
        message:
          `This revokes the current credential for ${license.customer || license.license_id} immediately. ` +
          'Pulls using the old secret will start failing within seconds. ' +
          'Make sure you have access to update wherever it is currently configured.',
        confirmLabel: 'Rotate',
        danger: true,
      });
      if (!ok) return;
    }
    setPending(true);
    setErr(null);
    try {
      const res = await catalog.rotateCredential(license.id);
      setRevealed(res);
    } catch (e) {
      setErr(e);
    } finally {
      setPending(false);
    }
  };

  const onCloseReveal = async () => {
    setRevealed(null);
    await refresh();
  };

  return (
    <section className="bg-g-elevated border border-g-border-weak rounded">
      <div className="px-5 py-4 border-b border-g-border-weak flex items-center justify-between gap-3 flex-wrap">
        <div className="min-w-0">
          <h2 className="text-sm font-semibold flex items-center gap-2">
            <MdKey className="text-base text-g-text-secondary" />
            {license.customer || 'License'}
            {license.tier && (
              <span className="chip-accent text-[10px]">{license.tier}</span>
            )}
          </h2>
          <p className="mt-0.5 text-xs text-g-text-disabled font-mono truncate">
            {license.license_id}
          </p>
        </div>
        <div className="flex items-center gap-2">
          {status === 'active' && (
            <Button
              variant="ghost"
              onClick={rotate}
              disabled={pending || !customerRotateEnabled}
              icon={<MdRefresh />}
              title={!customerRotateEnabled ? ROTATE_DISABLED_HINT : undefined}
            >
              {pending ? 'Rotating…' : 'Rotate'}
            </Button>
          )}
          {status === 'none' && (
            <Button
              variant="primary"
              onClick={rotate}
              disabled={pending || !customerRotateEnabled}
              icon={<MdAdd />}
              title={!customerRotateEnabled ? ROTATE_DISABLED_HINT : undefined}
            >
              {pending ? 'Creating…' : 'Create credential'}
            </Button>
          )}
        </div>
      </div>

      <div className="px-5 py-4 text-sm">
        <ErrorBanner error={err} />
        {status === 'loading' && <Spinner label="Loading credential" />}
        {status === 'active' && data && (
          <dl className="divide-y divide-g-border-weak">
            <Row label="Token ID"  value={<code className="font-mono">{data.token_id}</code>} />
            <Row label="Created"   value={data.created_at ? new Date(data.created_at).toLocaleString() : '—'} />
            <Row label="Last used" value={data.last_used_at ? new Date(data.last_used_at).toLocaleString() : <span className="text-g-text-disabled">never</span>} />
            <Row label="Expires"   value={data.expires_at ? new Date(data.expires_at).toLocaleString() : <span className="text-g-text-disabled">never</span>} />
          </dl>
        )}
        {status === 'none' && !err && (
          <p className="text-g-text-secondary">
            No credential issued yet. Click <strong>Create credential</strong> to generate one.
            You'll see the secret exactly once — save it before closing the dialog.
          </p>
        )}
        {status === 'error' && (
          <Button variant="ghost" onClick={refresh}>Try again</Button>
        )}
        {(status === 'active' || status === 'none') && !customerRotateEnabled && (
          <p className="mt-3 text-xs text-g-text-secondary">{ROTATE_DISABLED_HINT}</p>
        )}
      </div>

      <RevealedSecretModal
        open={!!revealed}
        license={license}
        revealed={revealed}
        onClose={onCloseReveal}
      />
    </section>
  );
}

// RevealedSecretModal shows the plaintext secret exactly once and gates the
// "Done" button behind a "I've saved this" checkbox so the user can't dismiss
// without acknowledging it. aria-live=assertive surfaces the secret to screen
// readers as soon as the modal opens.
function RevealedSecretModal({ open, license, revealed, onClose }) {
  const [ack, setAck] = useState(false);
  useEffect(() => {
    if (open) setAck(false);
  }, [open]);

  return (
    <Modal
      open={open}
      onClose={() => { /* require explicit Done */ }}
      title={`New credential for ${license?.customer || license?.license_id || 'license'}`}
      description="Copy this now. You won't be able to see it again."
      size="lg"
      footer={
        <>
          <label className="inline-flex items-center gap-2 text-xs text-g-text-secondary mr-auto">
            <input
              type="checkbox"
              checked={ack}
              onChange={(e) => setAck(e.target.checked)}
            />
            I've saved this credential.
          </label>
          <Button variant="primary" disabled={!ack} onClick={onClose}>Done</Button>
        </>
      }
    >
      {revealed && (
        <div className="space-y-4" aria-live="assertive">
          <div>
            <div className="text-xs uppercase tracking-wider font-medium text-g-text-secondary mb-1">Token ID</div>
            <CopyableCode value={revealed.token_id} />
          </div>
          <div>
            <div className="text-xs uppercase tracking-wider font-medium text-g-text-secondary mb-1">Secret</div>
            <CopyableCode value={revealed.secret} />
          </div>
          <div>
            <div className="text-xs uppercase tracking-wider font-medium text-g-text-secondary mb-1">docker login password</div>
            <CopyableCode value={revealed.full_credential} />
          </div>
          <p className="text-xs text-g-text-secondary">
            Use the docker login password as the password (any value works as
            the username) when configuring docker, helm, or buildx.
          </p>
        </div>
      )}
    </Modal>
  );
}

function Row({ label, value }) {
  return (
    <div className="px-0 py-3 flex items-baseline gap-4">
      <dt className="w-28 shrink-0 text-xs uppercase tracking-wider font-medium text-g-text-secondary">{label}</dt>
      <dd className="min-w-0 flex-1 text-g-text break-all">{value}</dd>
    </div>
  );
}
