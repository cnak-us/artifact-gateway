import clsx from 'clsx';

const paddings = { none: '', sm: 'p-3', md: 'p-4', lg: 'p-6' };

export default function Card({ children, padding = 'md', className = '', elevated = false }) {
  return (
    <div
      className={clsx(
        'border rounded',
        elevated ? 'bg-g-elevated shadow-z1' : 'bg-g-primary',
        'border-g-border-weak',
        paddings[padding] || paddings.md,
        className,
      )}
    >
      {children}
    </div>
  );
}
