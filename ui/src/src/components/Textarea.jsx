import { forwardRef } from 'react';
import clsx from 'clsx';

const Textarea = forwardRef(({
  label, error, className = '', id, hint, rows = 4, mono, ...rest
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
      <textarea
        ref={ref}
        id={inputId}
        rows={rows}
        aria-invalid={!!error || undefined}
        aria-describedby={errorId}
        className={clsx(
          'w-full bg-g-secondary border rounded px-3 py-2 text-sm text-g-text',
          'placeholder:text-g-text-disabled',
          'focus:border-g-accent-main focus:outline-none focus:ring-2 focus:ring-g-accent-main/40',
          'disabled:opacity-50 disabled:cursor-not-allowed',
          'resize-y',
          error ? 'border-g-red-main' : 'border-g-border-medium',
          mono && 'font-mono text-xs',
        )}
        {...rest}
      />
      {error && <p id={errorId} className="mt-1 text-xs text-g-red-text">{error}</p>}
      {!error && hint && <p className="mt-1 text-xs text-g-text-disabled">{hint}</p>}
    </div>
  );
});

Textarea.displayName = 'Textarea';
export default Textarea;
