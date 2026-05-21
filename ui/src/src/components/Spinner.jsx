import clsx from 'clsx';

const sizeMap = {
  sm: 'h-4 w-4 border-2',
  md: 'h-6 w-6 border-2',
  lg: 'h-9 w-9 border-[3px]',
};

export default function Spinner({ size = 'md', label, className = '' }) {
  return (
    <div className={clsx('flex flex-col items-center justify-center gap-3', className)}>
      <div
        className={clsx(
          'animate-spin rounded-full border-g-accent-main border-t-transparent',
          sizeMap[size] || sizeMap.md,
        )}
      />
      {label && <div className="text-xs text-g-text-secondary">{label}</div>}
    </div>
  );
}
