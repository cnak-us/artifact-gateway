import clsx from 'clsx';

const colors = {
  blue:   'bg-g-accent-main/15 text-g-accent-text border-g-accent-main/25',
  green:  'bg-g-green-main/15 text-g-green-text border-g-green-main/25',
  red:    'bg-g-red-main/15 text-g-red-text border-g-red-main/25',
  yellow: 'bg-g-yellow-main/15 text-g-yellow-text border-g-yellow-main/25',
  orange: 'bg-g-orange-main/15 text-g-orange-text border-g-orange-main/25',
  purple: 'bg-g-purple-main/15 text-g-purple-text border-g-purple-main/25',
  gray:   'bg-g-secondary text-g-text-secondary border-g-border-weak',
};

export default function Badge({ children, color = 'gray', className = '', icon }) {
  return (
    <span
      className={clsx(
        'inline-flex items-center gap-1 px-1.5 py-0.5 rounded border text-xs font-medium leading-tight',
        colors[color] || colors.gray,
        className,
      )}
    >
      {icon && <span className="shrink-0">{icon}</span>}
      {children}
    </span>
  );
}
