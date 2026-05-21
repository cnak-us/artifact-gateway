import { forwardRef } from 'react';
import clsx from 'clsx';

const Input = forwardRef(({
  type = 'text', label, error, prefix, suffix, className = '', id, hint, ...rest
}, ref) => {
  const errorId = error && id ? `${id}-error` : undefined;
  const inputId = id || rest.name;
  return (
    <div className={clsx('w-full', className)}>
      {label && (
        <label htmlFor={inputId} className="block text-xs font-medium text-g-text-secondary mb-1.5">
          {label}
        </label>
      )}
      <div className="relative">
        {prefix && (
          <div className="absolute inset-y-0 left-0 flex items-center pl-3 pointer-events-none text-g-text-disabled">
            {prefix}
          </div>
        )}
        <input
          ref={ref}
          id={inputId}
          type={type}
          aria-invalid={!!error || undefined}
          aria-describedby={errorId}
          className={clsx(
            'w-full bg-g-secondary border rounded px-3 py-2 text-sm text-g-text',
            'placeholder:text-g-text-disabled',
            'focus:border-g-accent-main focus:outline-none focus:ring-2 focus:ring-g-accent-main/40',
            'disabled:opacity-50 disabled:cursor-not-allowed',
            error ? 'border-g-red-main' : 'border-g-border-medium',
            prefix && 'pl-9',
            suffix && 'pr-9',
          )}
          {...rest}
        />
        {suffix && (
          <div className="absolute inset-y-0 right-0 flex items-center pr-3 pointer-events-none text-g-text-disabled">
            {suffix}
          </div>
        )}
      </div>
      {error && <p id={errorId} className="mt-1 text-xs text-g-red-text">{error}</p>}
      {!error && hint && <p className="mt-1 text-xs text-g-text-disabled">{hint}</p>}
    </div>
  );
});

Input.displayName = 'Input';
export default Input;
