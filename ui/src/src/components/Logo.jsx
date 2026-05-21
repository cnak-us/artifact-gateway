import { brand } from '../brand/index.js';

// Brand-driven mark. The actual SVG lives in the active brand preset
// (see src/brand/presets/*). Use currentColor by wrapping in a colored parent.
export default function Logo({ className = 'w-7 h-7' }) {
  const B = brand.Logo;
  return <B className={className} />;
}
