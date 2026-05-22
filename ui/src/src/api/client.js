// Fetch wrapper. Sends/receives JSON, includes cookies for session auth.
// Errors are typed as `{error: {code, message}}` to match the server envelope.

export class ApiError extends Error {
  constructor(status, code, message, body) {
    super(message || `HTTP ${status}`);
    this.status = status;
    this.code = code;
    this.body = body;
  }
}

export async function request(method, path, { body, headers, signal, raw } = {}) {
  const init = {
    method,
    credentials: 'include',
    signal,
    headers: { Accept: 'application/json', ...(headers || {}) },
  };
  if (body !== undefined) {
    if (body instanceof FormData || typeof body === 'string') {
      init.body = body;
    } else {
      init.body = JSON.stringify(body);
      init.headers['Content-Type'] = 'application/json';
    }
  }

  const res = await fetch(path, init);
  if (raw) return res;

  const contentType = res.headers.get('content-type') || '';
  const isJson = contentType.includes('application/json');
  const data = isJson ? await res.json().catch(() => null) : await res.text().catch(() => '');

  if (!res.ok) {
    const envelope = isJson && data && typeof data === 'object' ? data.error || data : null;
    throw new ApiError(
      res.status,
      envelope?.code || `http_${res.status}`,
      envelope?.message || (typeof data === 'string' ? data : res.statusText),
      data,
    );
  }
  return data;
}

export const api = {
  get:    (path, opts) => request('GET',    path, opts),
  post:   (path, body, opts) => request('POST',   path, { ...(opts || {}), body }),
  put:    (path, body, opts) => request('PUT',    path, { ...(opts || {}), body }),
  patch:  (path, body, opts) => request('PATCH',  path, { ...(opts || {}), body }),
  delete: (path, opts) => request('DELETE', path, opts),
  raw:    (method, path, opts) => request(method, path, { ...(opts || {}), raw: true }),
};

export default api;

// ---- admin endpoints (all under /api/v1) ----
export const admin = {
  login: (email, password) => api.post('/api/v1/auth/login', { email, password }),
  logout: () => api.post('/api/v1/auth/logout', {}),
  me: () => api.get('/api/v1/auth/me'),

  authConfig: () => api.get('/api/v1/auth/config'),

  oidcProvidersPublic: () => api.get('/api/v1/auth/oidc-providers'),
  // Legacy: oidcStartUrl(provider) — returns base URL without query params.
  // New:    oidcStartUrl(provider, { flow, returnTo }) — flexible URL builder.
  oidcStartUrl: (provider, opts) => {
    const base = `/api/v1/auth/oidc/${encodeURIComponent(provider)}/start`;
    if (!opts || typeof opts === 'string') return base; // back-compat: old callers pass nothing or returnTo string
    const params = new URLSearchParams();
    if (opts.flow)     params.set('flow',      opts.flow);
    if (opts.returnTo) params.set('return_to', opts.returnTo);
    const qs = params.toString();
    return qs ? `${base}?${qs}` : base;
  },

  listUpstreamCredentials: () => api.get('/api/v1/upstream-credentials'),
  createUpstreamCredential: (body) => api.post('/api/v1/upstream-credentials', body),
  deleteUpstreamCredential: (id) => api.delete(`/api/v1/upstream-credentials/${id}`),
  testUpstreamCredential: (id) => api.post(`/api/v1/upstream-credentials/${id}/test`, {}),

  listPackages: () => api.get('/api/v1/packages'),
  getPackage: (id) => api.get(`/api/v1/packages/${id}`),
  createPackage: (body) => api.post('/api/v1/packages', body),
  updatePackage: (id, body) => api.patch(`/api/v1/packages/${id}`, body),
  deletePackage: (id) => api.delete(`/api/v1/packages/${id}`),
  probePackage: (id) => api.post(`/api/v1/packages/${id}/probe`, {}),

  // Multi-container packages — admin-side CRUD over package_containers rows.
  // body for upsert: { alias, upstream_repo, display_name }. Server stamps
  // source='' for UI-created rows (distinct from manifest-managed rows).
  listPackageContainers: (packageId) => api.get(`/api/v1/packages/${packageId}/containers`),
  upsertPackageContainer: (packageId, body) =>
    api.post(`/api/v1/packages/${packageId}/containers`, body),
  deletePackageContainer: (packageId, alias) =>
    api.delete(`/api/v1/packages/${packageId}/containers/${encodeURIComponent(alias)}`),

  listLicenses: () => api.get('/api/v1/licenses'),
  getLicense: (id) => api.get(`/api/v1/licenses/${id}`),
  uploadLicense: (licBlob) => api.post('/api/v1/licenses', { lic_blob: licBlob }),
  issueLicense: (body) => api.post('/api/v1/licenses/issue', body),
  revokeLicense: (id) => api.delete(`/api/v1/licenses/${id}`),
  getLicenseGrants: (id) => api.get(`/api/v1/licenses/${id}/grants`),
  putLicenseGrants: (id, grants) => api.put(`/api/v1/licenses/${id}/grants`, { grants }),
  listContacts: (licenseId) => api.get(`/api/v1/licenses/${licenseId}/contacts`),
  addContact: (licenseId, email, name = '') => api.post(`/api/v1/licenses/${licenseId}/contacts`, { email, name }),
  removeContact: (licenseId, email) =>
    api.delete(`/api/v1/licenses/${licenseId}/contacts/${encodeURIComponent(email)}`),

  // Root signing keys. private_key_hex on createRootKey({mode:'generate'}) is
  // returned once and never stored in cleartext — the UI must show it to the
  // admin immediately and then drop it.
  listRootKeys: () => api.get('/api/v1/root-keys'),
  createRootKey: ({ name, mode, private_key_hex }) =>
    api.post('/api/v1/root-keys', { name, mode, private_key_hex }),
  activateRootKey: (id) => api.post(`/api/v1/root-keys/${id}/activate`, {}),
  deleteRootKey: (id) => api.delete(`/api/v1/root-keys/${id}`),

  listCustomerTokens: () => api.get('/api/v1/customer-tokens'),
  createCustomerToken: (body) => api.post('/api/v1/customer-tokens', body),
  deleteCustomerToken: (id) => api.delete(`/api/v1/customer-tokens/${id}`),
  previewCustomerToken: (id) => api.get(`/api/v1/customer-tokens/${id}/preview`),

  listOIDCProviders: () => api.get('/api/v1/oidc-providers'),
  createOIDCProvider: (body) => api.post('/api/v1/oidc-providers', body),
  deleteOIDCProvider: (id) => api.delete(`/api/v1/oidc-providers/${id}`),

  // Branding / white-label. getBranding hits the admin endpoint (authenticated);
  // the unauthed bootstrap at /api/branding is consumed by brand/index.js
  // directly, not via this helper.
  getBranding: () => api.get('/api/v1/branding'),
  putBranding: (body) => api.put('/api/v1/branding', body),

  // Server pagination is timestamp-cursor based: pass the oldest timestamp from
  // the current page as `before` to fetch the next older page. The `limit`
  // query param is accepted but the server caps it.
  auditEvents: ({ limit = 50, before } = {}) => {
    const params = new URLSearchParams({ limit: String(limit) });
    if (before) params.set('before', before);
    return api.get(`/api/v1/audit-events?${params.toString()}`);
  },

  // Declarative config (apiVersion/kind/metadata/spec) — declarative apply
  // surface. Export returns text/yaml with secrets rendered as "<redacted>".
  configExport: () => api.raw('GET', '/api/v1/config/export'),
  configApply: (manifestText, { dryRun = false, prune = false, contentType = 'text/yaml' } = {}) =>
    request('POST', `/api/v1/config/apply?dry_run=${dryRun}&prune=${prune}`, {
      body: manifestText,
      headers: { 'Content-Type': contentType, Accept: 'application/json' },
    }),

  // In-process metrics snapshots collected from the Prometheus registry.
  // The /metrics scrape endpoint is still served on the management port for
  // external Prometheus; these JSON endpoints back the admin UI charts.
  metricsCatalog: () => api.get('/api/v1/metrics/catalog'),
  metricsSeries: (name, { sinceSecs } = {}) => {
    const params = new URLSearchParams({ name });
    if (sinceSecs) params.set('since_secs', String(sinceSecs));
    return api.get(`/api/v1/metrics/series?${params.toString()}`);
  },

  // View-as-customer: mints an ag_customer_session bound to license_id. The
  // ag_admin_session cookie is untouched, so the admin keeps full access.
  // After this resolves, navigate to /catalog with a full page load so the
  // SPA picks up the new cookie.
  viewAsCustomer: (licenseId) =>
    api.post('/api/v1/view-as-customer', { license_id: licenseId }),
  endImpersonation: () => api.post('/api/v1/end-impersonation', {}),
};

// ---- catalog (customer) endpoints ----
export const catalog = {
  login: (tokenId, secret) => {
    const auth = typeof btoa === 'function'
      ? btoa(`${tokenId}:${secret}`)
      : Buffer.from(`${tokenId}:${secret}`).toString('base64');
    return request('POST', '/catalog/login', {
      headers: { Authorization: `Basic ${auth}` },
    });
  },
  logout: () => api.post('/catalog/logout', {}),
  me: () => api.get('/catalog/api/me'),
  listPackages: () => api.get('/catalog/api/packages'),
  getPackage: (slug) => api.get(`/catalog/api/packages/${encodeURIComponent(slug)}`),
  listTags: (slug) => api.get(`/catalog/api/packages/${encodeURIComponent(slug)}/tags`),

  // Multi-container catalog endpoints. listContainers returns alias rows for
  // packages that have multiple containers (empty array / 404 for single-repo
  // packages). listContainerTags returns the semver-desc-sorted tag list for
  // one alias under a package.
  listContainers: (slug) =>
    api.get(`/catalog/api/packages/${encodeURIComponent(slug)}/containers`),
  listContainerTags: (slug, alias) =>
    api.get(`/catalog/api/packages/${encodeURIComponent(slug)}/containers/${encodeURIComponent(alias)}/tags`),
  hostname: () => api.get('/catalog/api/hostname'),
  listDownloads: (slug) => api.get(`/download/${encodeURIComponent(slug)}`),
  signDownload: (slug, tag, asset) => api.post('/catalog/api/downloads/sign', { slug, tag, asset }),

  // URL of the .lic blob for the signed-in customer. Returned as a string so
  // callers can use it as an <a href>: the browser does a regular GET with the
  // session cookie attached, which fetch() can't do for a file download.
  downloadLicense: () => '/catalog/api/license',

  // Fetch the .lic blob as text — same endpoint as downloadLicense() but for
  // inline display / copy-to-clipboard in the credentials page.
  getLicenseBlob: async () => {
    const res = await fetch('/catalog/api/license', { credentials: 'include' });
    if (!res.ok) throw new ApiError(res.status, `http_${res.status}`, res.statusText);
    return res.text();
  },

  // OIDC sign-in for customers. The auth-code flow happens via full-page
  // redirects (not fetch), so we return URLs rather than calling them.
  oidcProviders: () => api.get('/catalog/oidc-providers'),
  oidcStartUrl: (provider, returnTo = '/catalog') =>
    `/catalog/oidc/${encodeURIComponent(provider)}/start?return_to=${encodeURIComponent(returnTo)}`,
};
