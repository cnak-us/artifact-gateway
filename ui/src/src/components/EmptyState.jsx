import clsx from 'clsx';

export default function EmptyState({ icon: Icon, title, description, action, className = '' }) {
  return (
    <div className={clsx('flex flex-col items-center justify-center py-16 px-6 text-g-text-secondary text-center', className)}>
      {Icon && (
        <div className="w-14 h-14 rounded bg-g-secondary border border-g-border-weak flex items-center justify-center mb-4">
          <Icon className="text-2xl opacity-50" />
        </div>
      )}
      {title && <p className="text-sm font-medium text-g-text mb-1">{title}</p>}
      {description && <p className="text-xs opacity-80 max-w-sm">{description}</p>}
      {action && <div className="mt-5">{action}</div>}
    </div>
  );
}
