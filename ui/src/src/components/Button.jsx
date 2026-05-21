import { forwardRef } from 'react';
import clsx from 'clsx';

const variants = {
  primary:   'bg-g-accent-main text-white hover:bg-g-accent-main/85 shadow-z1',
  secondary: 'bg-g-secondary text-g-text hover:bg-g-hover',
  outline:   'bg-transparent border border-g-border-medium text-g-text-secondary hover:text-g-text hover:bg-g-hover',
  ghost:     'bg-transparent text-g-text-secondary hover:text-g-text hover:bg-g-hover',
  danger:    'bg-transparent border border-g-red-main/30 text-g-red-text hover:bg-g-red-main/10',
};

const sizes = {
  sm: 'px-2.5 py-1 text-xs gap-1',
  md: 'px-3 py-1.5 text-sm gap-1.5',
  lg: 'px-4 py-2 text-sm gap-2',
};

const iconOnlySizes = {
  sm: 'p-1',
  md: 'p-1.5',
  lg: 'p-2',
};

const Button = forwardRef(({
  variant = 'secondary',
  size = 'md',
  icon,
  iconRight,
  loading,
  disabled,
  className = '',
  children,
  type = 'button',
  ...rest
}, ref) => {
  const isIconOnly = !!icon && !children && !iconRight;
  const isDisabled = disabled || loading;

  return (
    <button
      ref={ref}
      type={type}
      disabled={isDisabled}
      className={clsx(
        'inline-flex items-center justify-center rounded font-medium transition-colors select-none',
        'focus:outline-none focus-visible:ring-2 focus-visible:ring-g-accent-main/40',
        variants[variant] || variants.secondary,
        isIconOnly ? iconOnlySizes[size] : sizes[size],
        isDisabled ? 'opacity-50 cursor-not-allowed' : 'cursor-pointer',
        className,
      )}
      {...rest}
    >
      {loading ? (
        <Spinner size={size} />
      ) : icon ? <span className="shrink-0">{icon}</span> : null}
      {children}
      {iconRight && !loading && <span className="shrink-0">{iconRight}</span>}
    </button>
  );
});

Button.displayName = 'Button';
export default Button;

const Spinner = ({ size }) => {
  const dims = size === 'sm' ? 'h-3 w-3' : size === 'lg' ? 'h-4 w-4' : 'h-3.5 w-3.5';
  return (
    <svg className={`animate-spin ${dims}`} viewBox="0 0 24 24" fill="none">
      <circle className="opacity-25" cx="12" cy="12" r="10" stroke="currentColor" strokeWidth="4" />
      <path className="opacity-75" fill="currentColor" d="M4 12a8 8 0 018-8V0C5.373 0 0 5.373 0 12h4z" />
    </svg>
  );
};
