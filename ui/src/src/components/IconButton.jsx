import { forwardRef } from 'react';
import clsx from 'clsx';

const sizes = { sm: 'p-1', md: 'p-1.5', lg: 'p-2' };

const IconButton = forwardRef(({
  icon, size = 'md', label, className = '', variant = 'ghost', ...rest
}, ref) => {
  const variantCls = variant === 'danger'
    ? 'text-g-red-text hover:bg-g-red-main/10'
    : 'text-g-text-secondary hover:text-g-text hover:bg-g-hover';
  return (
    <button
      ref={ref}
      type="button"
      aria-label={label}
      title={label}
      className={clsx(
        'inline-flex items-center justify-center rounded transition-colors',
        'focus:outline-none focus-visible:ring-2 focus-visible:ring-g-accent-main/40',
        variantCls,
        sizes[size],
        className,
      )}
      {...rest}
    >
      {icon}
    </button>
  );
});

IconButton.displayName = 'IconButton';
export default IconButton;
