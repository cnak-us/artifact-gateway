import clsx from 'clsx';

// Headless table with CNAK styling. Pass `columns` and `rows`.
//   columns: [{ key, header, render?, className?, headerClassName?, width? }]
//   rows:    [object]; row identity defaults to row.id
//   onRowClick(row): optional row-level click handler

export default function Table({
  columns,
  rows,
  rowKey = 'id',
  onRowClick,
  empty,
  className = '',
  dense = false,
}) {
  return (
    <div className={clsx('overflow-hidden border border-g-border-weak rounded bg-g-primary', className)}>
      <div className="overflow-x-auto">
        <table className="w-full text-sm border-collapse">
          <thead>
            <tr className="bg-g-secondary">
              {columns.map((c) => (
                <th
                  key={c.key}
                  style={c.width ? { width: c.width } : undefined}
                  className={clsx(
                    'text-left text-[11px] font-semibold uppercase tracking-wider text-g-text-secondary',
                    'px-3 py-2 border-b border-g-border-weak',
                    c.headerClassName,
                  )}
                >
                  {c.header}
                </th>
              ))}
            </tr>
          </thead>
          <tbody>
            {rows.length === 0 ? (
              <tr>
                <td colSpan={columns.length} className="px-3 py-8 text-center text-g-text-disabled text-sm">
                  {empty || 'No records'}
                </td>
              </tr>
            ) : (
              rows.map((row) => {
                const key = typeof rowKey === 'function' ? rowKey(row) : row[rowKey];
                return (
                  <tr
                    key={key}
                    onClick={onRowClick ? () => onRowClick(row) : undefined}
                    className={clsx(
                      'border-b border-g-border-weak last:border-b-0',
                      onRowClick && 'cursor-pointer hover:bg-g-hover transition-colors',
                    )}
                  >
                    {columns.map((c) => (
                      <td
                        key={c.key}
                        className={clsx(
                          'text-g-text align-middle',
                          dense ? 'px-3 py-1.5' : 'px-3 py-2.5',
                          c.className,
                        )}
                      >
                        {c.render ? c.render(row) : row[c.key]}
                      </td>
                    ))}
                  </tr>
                );
              })
            )}
          </tbody>
        </table>
      </div>
    </div>
  );
}
