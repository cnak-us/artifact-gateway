import { brand } from '../../brand/index.js';

// NoLicenseGate is rendered by CatalogLayout when the session is authenticated
// (Dex login succeeded) but the resolved license set is empty. The catalog
// surface is intentionally NOT mounted in this state — no package grid, no
// credential page, no nav link — so the only path forward is the support
// mailto. Backend mirrors this gate: /catalog/api/packages returns 403 for
// unlicensed sessions and `me.is_licensed` is false.
export default function NoLicenseGate({ session }) {
  const supportEmail = brand.supportEmail || '';
  const subjectIdentity = session?.token_id || session?.customer || 'license request';
  const mailto = supportEmail
    ? `mailto:${supportEmail}?subject=${encodeURIComponent('Access request: ' + subjectIdentity)}`
    : null;

  return (
    <div className="min-h-[60vh] flex items-center justify-center px-4">
      <div className="max-w-md w-full text-center">
        <div className="flex justify-center mb-6">
          <brand.Logo className="w-12 h-12 text-g-accent-text" />
        </div>
        <h1 className="text-xl font-semibold text-g-text mb-2">
          No license on file for this account
        </h1>
        <p className="text-sm text-g-text-secondary mb-6">
          You're signed in, but your account isn't associated with any active
          license yet. Reach out to support and we'll get you set up.
        </p>
        {mailto ? (
          <a
            href={mailto}
            className="inline-flex items-center justify-center px-4 py-2 rounded font-medium text-sm bg-g-accent-main/15 text-g-accent-text hover:bg-g-accent-main/25 transition-colors"
          >
            Contact support
          </a>
        ) : (
          <p className="text-xs text-g-text-disabled">
            Support contact isn't configured yet. Reach out through your usual channel.
          </p>
        )}
        {supportEmail && (
          <p className="mt-3 text-xs text-g-text-disabled font-mono">{supportEmail}</p>
        )}
      </div>
    </div>
  );
}
