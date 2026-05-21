import { useEffect, useState } from 'react';
import { MdMail, MdWarning, MdDownload, MdKey, MdAccountCircle } from 'react-icons/md';
import { useCatalogAuth } from '../../contexts/CatalogAuthContext.jsx';
import { catalog } from '../../api/client.js';
import CopyableCode from '../../components/CopyableCode.jsx';
import Spinner from '../../components/Spinner.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';

// "How did this session arrive" — basic-auth customers carry an opaque token
// id (e.g. "BB7Q4SCEEIANCDABRP3K"). OIDC contacts are stamped with their email
// in that slot by the catalog session layer. We treat an "@" in the token_id
// as the OIDC tell rather than plumbing a dedicated server field.
function detectSessionKind(session) {
  const tid = session?.token_id || session?.tokenId || '';
  if (tid.includes('@')) return { kind: 'oidc', identity: tid };
  return { kind: 'token', identity: tid };
}

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

  if (!session) return null;

  const license = session.license || {};
  const licenseId = session.license_id || license.id || license.license_id;
  const customer = license.customer || session.customer;
  const organization = license.organization || session.organization;
  const tier = license.tier || session.tier;
  const expiresLicense = license.expires_at || session.expires_at;
  const expiresToken = session.token_expires_at;
  const lastUsed = session.last_used_at;
  const { kind, identity } = detectSessionKind(session);

  return (
    <div className="max-w-3xl mx-auto px-6 py-10">
      <header className="mb-8">
        <h1 className="text-2xl font-semibold tracking-tight">My credential</h1>
        <p className="mt-2 text-sm text-g-text-secondary max-w-xl">
          What you're signed in with, and the license that backs your access.
        </p>
      </header>

      {/* Identity — adapts to OIDC vs token sessions. The basic-auth path
          carries a docker-pull credential; the OIDC contact path doesn't. */}
      <section className="bg-g-elevated border border-g-border-weak rounded">
        <div className="px-5 py-4 border-b border-g-border-weak flex items-center gap-2">
          {kind === 'oidc' ? <MdAccountCircle /> : <MdKey />}
          <h2 className="text-sm font-semibold">
            {kind === 'oidc' ? 'Signed in as' : 'Token'}
          </h2>
        </div>
        <dl className="divide-y divide-g-border-weak text-sm">
          {kind === 'oidc' ? (
            <>
              <Row label="Email"   value={<code className="font-mono">{identity || '—'}</code>} />
              <Row label="Method"  value={<span className="chip">OIDC sign-in</span>} />
              <Row
                label="Note"
                value={
                  <span className="text-g-text-secondary">
                    You're on the trusted-contact allowlist for this license. To pull images
                    with <code className="text-xs">docker login</code>, ask your administrator
                    for a customer token.
                  </span>
                }
              />
            </>
          ) : (
            <>
              <Row label="Token ID"  value={<code className="font-mono">{identity || '—'}</code>} />
              <Row label="Secret"    value={<span className="text-g-text-disabled italic">hidden — only shown at issue time</span>} />
              <Row label="Expires"   value={expiresToken ? new Date(expiresToken).toLocaleString() : <span className="text-g-text-disabled">never</span>} />
              <Row label="Last used" value={lastUsed ? new Date(lastUsed).toLocaleString() : <span className="text-g-text-disabled">just now</span>} />
            </>
          )}
        </dl>
      </section>

      {/* License metadata — surfaces License ID (was previously hidden). */}
      <section className="bg-g-elevated border border-g-border-weak rounded mt-6">
        <div className="px-5 py-4 border-b border-g-border-weak">
          <h2 className="text-sm font-semibold">License</h2>
        </div>
        <dl className="divide-y divide-g-border-weak text-sm">
          <Row label="License ID" value={licenseId ? <code className="font-mono text-xs">{licenseId}</code> : '—'} />
          <Row label="Customer"     value={customer || '—'} />
          <Row label="Organization" value={organization || '—'} />
          <Row label="Tier"         value={tier ? <span className="chip-accent">{tier}</span> : '—'} />
          <Row label="Expires"      value={expiresLicense ? new Date(expiresLicense).toLocaleDateString() : <span className="text-g-text-disabled">never</span>} />
        </dl>
      </section>

      {/* License file — raw .lic blob, copyable + downloadable. The button is
          the proper way to save the file; the inline view is for quick copy. */}
      <section className="bg-g-elevated border border-g-border-weak rounded mt-6">
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

      {kind === 'token' && (
        <section className="mt-8 p-4 bg-g-yellow-main/10 border border-g-yellow-main/30 rounded text-sm flex items-start gap-3">
          <MdWarning className="text-g-yellow-text shrink-0 mt-0.5" />
          <div className="text-g-text">
            <p className="font-medium">Need to rotate this credential?</p>
            <p className="mt-1 text-g-text-secondary">
              Secret rotation is handled by your account manager. Contact us and we'll
              issue a new token and revoke the old one.
            </p>
            <a
              href="mailto:support@cnak.us"
              className="mt-3 inline-flex items-center gap-1.5 px-3 py-1.5 rounded text-xs font-medium bg-g-elevated border border-g-border-medium text-g-text hover:bg-g-hover transition-colors"
            >
              <MdMail /> support@cnak.us
            </a>
          </div>
        </section>
      )}
    </div>
  );
}

function Row({ label, value }) {
  return (
    <div className="px-5 py-3 flex items-baseline gap-4">
      <dt className="w-32 shrink-0 text-xs uppercase tracking-wider font-medium text-g-text-secondary">{label}</dt>
      <dd className="min-w-0 flex-1 text-g-text break-all">{value}</dd>
    </div>
  );
}
