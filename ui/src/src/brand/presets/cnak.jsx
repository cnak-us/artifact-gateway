// CNAK brand preset. The Logo SVG is copied verbatim from components/Logo.jsx
// so swapping brands never touches that component.
function Logo({ className = 'w-7 h-7' }) {
  return (
    <svg
      viewBox="0 0 40 40"
      xmlns="http://www.w3.org/2000/svg"
      className={className}
      aria-label="CNAK artifact-gateway"
      role="img"
      fill="none"
      stroke="currentColor"
    >
      <path
        d="M20 3L33.29 9.4L36.57 23.81L27.38 35.32L12.62 35.32L3.43 23.81L6.71 9.4Z"
        strokeWidth="3"
        strokeLinejoin="round"
      />
      <circle cx="20" cy="20" r="5" strokeWidth="2.5" />
      <circle cx="20" cy="20" r="2.5" fill="currentColor" stroke="none" />
      <line x1="20" y1="20" x2="28" y2="13" strokeWidth="2.5" strokeLinecap="round" />
    </svg>
  );
}

/** @type {import('../types.js').Brand} */
const cnak = {
  productName: 'Artifact Gateway',
  vendor: 'CNAK Distribution',
  vendorShort: 'CNAK',
  footerTagline: 'CNAK · Crummy Solutions',
  embeddedTagline: 'artifact-gateway · embedded',
  catalogHeroEyebrow: 'Your CNAK distribution',
  htmlTitle: 'Artifact Gateway · CNAK',
  metaDescription: 'CNAK Artifact Gateway — licensed OCI distribution',
  themeStorageKey: 'cnak.theme',
  accent: {
    light: { main: '56 113 220', text: '31 98 224' },
    dark: { main: '61 113 217', text: '110 159 255' },
  },
  Logo,
};

export default cnak;
