// Bayplan mark (docs/brand/bayplan-mark.svg): a 3×3 bay with two gaps at the
// top right and an orange container, "every container in its place". The
// filled squares use currentColor so the same mark works in white on the dark
// topbar and in the `brand` grey on a light background; the orange (Container
// Orange) is fixed: it is the brand's only accent and must not change with
// the theme.

const BRAND_ORANGE = '#E8682A'

export function Logo({ className }: { className?: string }) {
  const cells: Array<[number, number, string | undefined]> = [
    [9, 9, undefined],
    [47, 9, undefined],
    [85, 9, 'faded'],
    [9, 47, undefined],
    [47, 47, undefined],
    [85, 47, 'accent'],
    [9, 85, undefined],
    [47, 85, undefined],
    [85, 85, undefined],
  ]
  return (
    <svg viewBox="0 0 126 126" className={className} aria-hidden="true">
      {cells.map(([x, y, kind]) => (
        <rect
          key={`${x}-${y}`}
          x={x}
          y={y}
          width="32"
          height="32"
          rx="4"
          fill={kind === 'accent' ? BRAND_ORANGE : 'currentColor'}
          opacity={kind === 'faded' ? 0.25 : undefined}
        />
      ))}
    </svg>
  )
}
