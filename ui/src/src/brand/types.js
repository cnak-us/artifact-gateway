/**
 * @typedef {Object} BrandAccent
 * @property {string} main  RGB triplet, space-separated (e.g. "56 113 220"), matches --g-accent-main convention
 * @property {string} text  RGB triplet for --g-accent-text
 */

/**
 * @typedef {Object} Brand
 * @property {string} productName        e.g. "Artifact Gateway"
 * @property {string} vendor             TopBar primary line, e.g. "CNAK Distribution"
 * @property {string} vendorShort        e.g. "CNAK"
 * @property {string} footerTagline      e.g. "CNAK · Crummy Solutions"
 * @property {string} embeddedTagline    Sidebar footer, e.g. "artifact-gateway · embedded"
 * @property {string} catalogHeroEyebrow e.g. "Your CNAK distribution"
 * @property {string} htmlTitle          <title> value
 * @property {string} metaDescription    <meta name="description">
 * @property {string} themeStorageKey    localStorage key for theme persistence
 * @property {{ light: BrandAccent, dark: BrandAccent }} accent
 * @property {React.ComponentType<{ className?: string }>} Logo  Accepts className, uses currentColor
 */
export {};
