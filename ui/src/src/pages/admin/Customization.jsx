import { useEffect, useMemo, useRef, useState } from 'react';
import { MdSave, MdRestore, MdReplay, MdFileUpload, MdClear } from 'react-icons/md';
import { admin } from '../../api/client.js';
import { brand, loadRuntimeBrand } from '../../brand/index.js';
import { useToast } from '../../components/Toast.jsx';
import Button from '../../components/Button.jsx';
import Input from '../../components/Input.jsx';
import Textarea from '../../components/Textarea.jsx';
import Spinner from '../../components/Spinner.jsx';
import ErrorBanner from '../../components/ErrorBanner.jsx';

const EMPTY = {
  product_name: '', vendor: '', vendor_short: '',
  html_title: '', meta_description: '',
  footer_tagline: '', embedded_tagline: '', catalog_hero_eyebrow: '',
  accent_light_main: '', accent_light_text: '',
  accent_dark_main: '', accent_dark_text: '',
  logo_svg: '',
};

// Field placeholders sourced from the CNAK preset so admins see what the
// "default" looks like without having to dig through the codebase.
const PLACEHOLDERS = {
  product_name: 'Artifact Gateway',
  vendor: 'CNAK Distribution',
  vendor_short: 'CNAK',
  html_title: 'Artifact Gateway · CNAK',
  meta_description: 'CNAK Artifact Gateway — licensed OCI distribution',
  footer_tagline: 'CNAK · Crummy Solutions',
  embedded_tagline: 'artifact-gateway · embedded',
  catalog_hero_eyebrow: 'Your CNAK distribution',
  accent_light_main: '56 113 220',
  accent_light_text: '31 98 224',
  accent_dark_main: '61 113 217',
  accent_dark_text: '110 159 255',
};

function tripletToHex(t) {
  const m = /^\s*(\d{1,3})\s+(\d{1,3})\s+(\d{1,3})\s*$/.exec(t || '');
  if (!m) return '#000000';
  const [r, g, b] = [+m[1], +m[2], +m[3]].map((v) => Math.max(0, Math.min(255, v)));
  return '#' + [r, g, b].map((v) => v.toString(16).padStart(2, '0')).join('');
}

function hexToTriplet(hex) {
  const m = /^#([0-9a-f]{6})$/i.exec((hex || '').trim());
  if (!m) return '';
  const n = parseInt(m[1], 16);
  return `${(n >> 16) & 255} ${(n >> 8) & 255} ${n & 255}`;
}

function tripletToCss(t) {
  const m = /^\s*(\d{1,3})\s+(\d{1,3})\s+(\d{1,3})\s*$/.exec(t || '');
  return m ? `rgb(${m[1]}, ${m[2]}, ${m[3]})` : 'rgb(56, 113, 220)';
}

function tripletToRgba(t, alpha) {
  const m = /^\s*(\d{1,3})\s+(\d{1,3})\s+(\d{1,3})\s*$/.exec(t || '');
  if (!m) return `rgba(56, 113, 220, ${alpha})`;
  return `rgba(${m[1]}, ${m[2]}, ${m[3]}, ${alpha})`;
}

// Normalize loaded payload — any missing/non-string field becomes ''.
function normalize(payload) {
  const out = { ...EMPTY };
  if (!payload || typeof payload !== 'object') return out;
  for (const k of Object.keys(EMPTY)) {
    if (typeof payload[k] === 'string') out[k] = payload[k];
  }
  return out;
}

export default function Customization() {
  const toast = useToast();

  const [loading, setLoading] = useState(true);
  const [loadErr, setLoadErr] = useState(null);
  const [saveErr, setSaveErr] = useState(null);
  const [saving, setSaving] = useState(false);
  const [snapshot, setSnapshot] = useState(EMPTY);
  const [form, setForm] = useState(EMPTY);
  const [previewMode, setPreviewMode] = useState(
    typeof document !== 'undefined' && document.documentElement.classList.contains('dark') ? 'dark' : 'light',
  );
  const fileRef = useRef(null);
  const [drag, setDrag] = useState(false);

  const onSvgFile = async (e) => {
    const file = e.target.files?.[0];
    if (fileRef.current) fileRef.current.value = ''; // allow re-picking the same file
    if (!file) return;
    if (file.size > 64 * 1024) {
      toast.error('Logo SVG exceeds the 64 KiB limit.');
      return;
    }
    try {
      const text = await file.text();
      const trimmed = text.trim();
      if (!trimmed.startsWith('<svg')) {
        toast.error('File does not look like an SVG (missing <svg root).');
        return;
      }
      setForm((f) => ({ ...f, logo_svg: trimmed }));
      toast.success('Logo SVG loaded from file');
    } catch (err) {
      toast.error('Could not read file: ' + (err.message || err));
    }
  };

  const onClearLogo = () => setForm((f) => ({ ...f, logo_svg: '' }));

  useEffect(() => {
    let cancelled = false;
    (async () => {
      setLoading(true);
      setLoadErr(null);
      try {
        const res = await admin.getBranding();
        if (cancelled) return;
        const n = normalize(res);
        setSnapshot(n);
        setForm(n);
      } catch (e) {
        if (!cancelled) setLoadErr(e);
      } finally {
        if (!cancelled) setLoading(false);
      }
    })();
    return () => { cancelled = true; };
  }, []);

  const dirty = useMemo(() => {
    for (const k of Object.keys(EMPTY)) {
      if ((form[k] || '') !== (snapshot[k] || '')) return true;
    }
    return false;
  }, [form, snapshot]);

  const set = (k) => (e) => setForm((f) => ({ ...f, [k]: e.target.value }));

  const onRevert = () => {
    setForm(snapshot);
    setSaveErr(null);
  };

  const onResetDefaults = () => {
    setForm(EMPTY);
    setSaveErr(null);
  };

  const onSave = async () => {
    setSaveErr(null);
    setSaving(true);
    try {
      const saved = await admin.putBranding(form);
      const n = normalize(saved || form);
      setSnapshot(n);
      setForm(n);
      // Re-hydrate the in-page brand so logo/text in the chrome update without reload.
      await loadRuntimeBrand();
      toast.success('Branding updated');
    } catch (e) {
      // Keep validation errors inline; everything else still surfaces in the banner.
      setSaveErr(e);
    } finally {
      setSaving(false);
    }
  };

  if (loading) {
    return (
      <div className="space-y-4">
        <PageHeader />
        <Spinner label="Loading branding" />
      </div>
    );
  }

  return (
    <div className="space-y-4">
      <PageHeader />

      <ErrorBanner error={loadErr} />
      <ErrorBanner error={saveErr} />

      <div className="grid grid-cols-1 lg:grid-cols-[minmax(0,1fr)_minmax(0,420px)] gap-4 items-start">
        <div className="space-y-4 min-w-0">
          <Section title="Identity">
            <Input label="Product name" value={form.product_name} onChange={set('product_name')} placeholder={PLACEHOLDERS.product_name} />
            <Input label="Vendor" value={form.vendor} onChange={set('vendor')} placeholder={PLACEHOLDERS.vendor} />
            <Input label="Vendor short" value={form.vendor_short} onChange={set('vendor_short')} placeholder={PLACEHOLDERS.vendor_short} />
            <Input label="HTML title" value={form.html_title} onChange={set('html_title')} placeholder={PLACEHOLDERS.html_title} />
            <Input label="Meta description" value={form.meta_description} onChange={set('meta_description')} placeholder={PLACEHOLDERS.meta_description} />
          </Section>

          <Section title="Layout text">
            <Input label="Footer tagline" value={form.footer_tagline} onChange={set('footer_tagline')} placeholder={PLACEHOLDERS.footer_tagline} />
            <Input label="Sidebar embedded tagline" value={form.embedded_tagline} onChange={set('embedded_tagline')} placeholder={PLACEHOLDERS.embedded_tagline} />
            <Input label="Catalog hero eyebrow" value={form.catalog_hero_eyebrow} onChange={set('catalog_hero_eyebrow')} placeholder={PLACEHOLDERS.catalog_hero_eyebrow} />
          </Section>

          <Section title="Accent colors">
            <div className="grid grid-cols-1 sm:grid-cols-2 gap-3">
              <ColorField label="Light · main" triplet={form.accent_light_main} onChange={(v) => setForm((f) => ({ ...f, accent_light_main: v }))} placeholder={PLACEHOLDERS.accent_light_main} />
              <ColorField label="Light · text" triplet={form.accent_light_text} onChange={(v) => setForm((f) => ({ ...f, accent_light_text: v }))} placeholder={PLACEHOLDERS.accent_light_text} />
              <ColorField label="Dark · main" triplet={form.accent_dark_main} onChange={(v) => setForm((f) => ({ ...f, accent_dark_main: v }))} placeholder={PLACEHOLDERS.accent_dark_main} />
              <ColorField label="Dark · text" triplet={form.accent_dark_text} onChange={(v) => setForm((f) => ({ ...f, accent_dark_text: v }))} placeholder={PLACEHOLDERS.accent_dark_text} />
            </div>
          </Section>

          <Section title="Logo (SVG)">
            <div className="flex items-center gap-1 flex-wrap">
              <Button
                variant="outline"
                size="sm"
                icon={<MdFileUpload />}
                onClick={() => fileRef.current?.click()}
              >
                Upload SVG…
              </Button>
              {form.logo_svg && (
                <Button
                  variant="ghost"
                  size="sm"
                  icon={<MdClear />}
                  onClick={onClearLogo}
                >
                  Clear
                </Button>
              )}
              <input
                ref={fileRef}
                type="file"
                accept=".svg,image/svg+xml"
                className="hidden"
                onChange={onSvgFile}
              />
            </div>
            <div
              onDragOver={(e) => { e.preventDefault(); setDrag(true); }}
              onDragLeave={() => setDrag(false)}
              onDrop={(e) => {
                e.preventDefault();
                setDrag(false);
                const file = e.dataTransfer.files?.[0];
                if (file) onSvgFile({ target: { files: [file] } });
              }}
              className={
                'relative rounded border-2 border-dashed transition-colors ' +
                (drag
                  ? 'border-g-accent-main bg-g-accent-main/5'
                  : 'border-g-border-medium')
              }
            >
              <Textarea
                mono
                rows={12}
                value={form.logo_svg}
                onChange={set('logo_svg')}
                placeholder='<svg viewBox="0 0 40 40" xmlns="http://www.w3.org/2000/svg" fill="none" stroke="currentColor">...</svg>'
                spellCheck={false}
              />
              {!form.logo_svg && (
                <div className="pointer-events-none absolute inset-x-0 bottom-2 text-center text-xs text-g-text-secondary">
                  {drag ? 'Drop SVG to load' : 'Drop an .svg file here, or paste markup above'}
                </div>
              )}
            </div>
            <p className="mt-1 text-xs text-g-text-secondary">
              Paste markup or upload a file (.svg, ≤ 64 KiB). The SVG should use{' '}
              <span className="font-mono">currentColor</span> for fill/stroke so it adopts
              the active accent.
            </p>
          </Section>

          <div className="flex flex-wrap items-center gap-2 pt-1">
            <Button
              variant="primary"
              icon={<MdSave />}
              onClick={onSave}
              loading={saving}
              disabled={!dirty || saving}
            >
              Save
            </Button>
            <Button
              variant="outline"
              icon={<MdRestore />}
              onClick={onRevert}
              disabled={!dirty || saving}
            >
              Revert
            </Button>
            <Button
              variant="ghost"
              icon={<MdReplay />}
              onClick={onResetDefaults}
              disabled={saving}
              title="Clear every field — on save the backend falls back to the preset defaults."
            >
              Reset to defaults
            </Button>
            {!dirty && (
              <span className="text-[11px] text-g-text-disabled">No changes</span>
            )}
          </div>
        </div>

        <PreviewPane form={form} mode={previewMode} onModeChange={setPreviewMode} />
      </div>
    </div>
  );
}

function PageHeader() {
  return (
    <div>
      <h1 className="text-xl font-semibold">Customization</h1>
      <p className="text-sm text-g-text-secondary max-w-2xl">
        White-label the admin and catalog surfaces. Empty fields fall back to the
        default brand preset shipped with the build.
      </p>
    </div>
  );
}

function Section({ title, children }) {
  return (
    <section className="rounded border border-g-border-weak bg-g-primary p-4">
      <h2 className="text-sm font-semibold text-g-text mb-3">{title}</h2>
      <div className="space-y-3">{children}</div>
    </section>
  );
}

function ColorField({ label, triplet, onChange, placeholder }) {
  const hex = tripletToHex(triplet || placeholder || '0 0 0');
  return (
    <div>
      <label className="block text-xs font-medium text-g-text-secondary mb-1.5">{label}</label>
      <div className="flex items-center gap-2">
        <input
          type="color"
          value={hex}
          onChange={(e) => onChange(hexToTriplet(e.target.value))}
          className="h-9 w-12 rounded border border-g-border-medium bg-g-secondary cursor-pointer p-0.5"
          aria-label={`${label} color picker`}
        />
        <input
          type="text"
          value={triplet}
          onChange={(e) => onChange(e.target.value)}
          placeholder={placeholder}
          className="flex-1 min-w-0 bg-g-secondary border border-g-border-medium rounded px-3 py-2 text-sm font-mono text-g-text placeholder:text-g-text-disabled focus:border-g-accent-main focus:outline-none focus:ring-2 focus:ring-g-accent-main/40"
        />
      </div>
    </div>
  );
}

function PreviewPane({ form, mode, onModeChange }) {
  const accentMain = mode === 'dark' ? form.accent_dark_main : form.accent_light_main;
  const accentText = mode === 'dark' ? form.accent_dark_text : form.accent_light_text;
  // Fall back to the live brand's value when the field is blank so the
  // preview matches what the saved configuration would render.
  const effectiveMain = accentMain || brand.accent[mode]?.main || '';
  const effectiveText = accentText || brand.accent[mode]?.text || '';
  const mainCss = tripletToCss(effectiveMain);
  const textCss = tripletToCss(effectiveText);
  const mainBgSoft  = tripletToRgba(effectiveMain, 0.08);
  const mainBorder  = tripletToRgba(effectiveMain, 0.2);

  const productName = form.product_name || brand.productName;
  const vendor      = form.vendor       || brand.vendor;
  const footer      = form.footer_tagline || brand.footerTagline;

  // If the admin pasted SVG markup, render it raw; otherwise fall back to
  // the currently loaded brand's Logo component.
  const hasLogoSvg = (form.logo_svg || '').trim().startsWith('<svg');
  const LiveLogo = brand.Logo;

  // Surface area is light-on-light or dark-on-dark depending on the toggle;
  // we don't flip the whole app, just this card.
  const surfaceBg = mode === 'dark' ? '#1a1d24' : '#ffffff';
  const surfaceBorder = mode === 'dark' ? '#2a2f3a' : '#e4e7ec';
  const surfaceText = mode === 'dark' ? '#e6e8ee' : '#1a1d24';
  const surfaceSubtext = mode === 'dark' ? '#9aa3b2' : '#6b7280';

  return (
    <aside className="lg:sticky lg:top-4 rounded border border-g-border-weak bg-g-primary">
      <div className="px-3 py-2 border-b border-g-border-weak flex items-center justify-between">
        <h2 className="text-sm font-semibold text-g-text">Live preview</h2>
        <div className="inline-flex rounded border border-g-border-weak overflow-hidden text-xs">
          {['light', 'dark'].map((m) => (
            <button
              key={m}
              type="button"
              onClick={() => onModeChange(m)}
              className={
                'px-2.5 py-1 font-medium transition-colors ' +
                (mode === m
                  ? 'bg-g-accent-main text-white'
                  : 'bg-transparent text-g-text-secondary hover:text-g-text hover:bg-g-hover')
              }
            >
              {m === 'light' ? 'Light' : 'Dark'}
            </button>
          ))}
        </div>
      </div>

      <div className="p-3">
        <div
          className="rounded border overflow-hidden"
          style={{
            backgroundColor: surfaceBg,
            borderColor: surfaceBorder,
            color: surfaceText,
            // Scope the preview accent to this subtree so we don't leak into the real app.
            '--preview-accent-main': mainCss,
            '--preview-accent-text': textCss,
          }}
        >
          <div
            className="flex items-center gap-2.5 px-3 py-2.5 border-b"
            style={{ borderColor: surfaceBorder }}
          >
            <span
              className="inline-flex shrink-0"
              style={{ color: textCss, width: 28, height: 28 }}
            >
              {hasLogoSvg ? (
                <span
                  style={{ display: 'inline-flex', width: '100%', height: '100%' }}
                  dangerouslySetInnerHTML={{ __html: form.logo_svg }}
                />
              ) : LiveLogo ? (
                <LiveLogo className="w-7 h-7" />
              ) : null}
            </span>
            <div className="min-w-0">
              <div className="text-sm font-semibold truncate" style={{ color: surfaceText }}>
                {vendor}
              </div>
              <div className="text-[11px] truncate" style={{ color: surfaceSubtext }}>
                Admin console
              </div>
            </div>
          </div>

          <div className="px-3 py-4 space-y-3">
            <div>
              <div className="text-[10px] uppercase tracking-wider" style={{ color: surfaceSubtext }}>
                {form.catalog_hero_eyebrow || brand.catalogHeroEyebrow}
              </div>
              <div className="text-base font-semibold mt-0.5" style={{ color: surfaceText }}>
                {productName}
              </div>
            </div>
            <button
              type="button"
              style={{ backgroundColor: mainCss, color: 'white' }}
              className="px-3 py-1.5 rounded text-sm font-medium"
            >
              Sample
            </button>
            <div>
              <span
                className="inline-block text-xs px-2 py-0.5 rounded"
                style={{ color: textCss, backgroundColor: mainBgSoft, border: `1px solid ${mainBorder}` }}
              >
                {form.embedded_tagline || brand.embeddedTagline}
              </span>
            </div>
          </div>

          <div
            className="px-3 py-2 text-[11px] border-t text-center"
            style={{ borderColor: surfaceBorder, color: surfaceSubtext }}
          >
            {footer}
          </div>
        </div>

        <p className="mt-2 text-[11px] text-g-text-disabled">
          Preview reflects unsaved edits. Saving applies the change to the live UI.
        </p>
      </div>
    </aside>
  );
}
