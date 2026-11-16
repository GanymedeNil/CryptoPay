import { cn } from "@/lib/utils";

/**
 * Guilloché security pattern (the fine interlaced line-work printed on
 * banknotes and cheques). Purely decorative: generated once at module load,
 * drawn with `currentColor` so the theme controls it.
 */

const r2 = (n: number) => Math.round(n * 100) / 100;

function wavePath(i: number, width: number, height: number): string {
  const mid = height / 2;
  const steps = 160;
  let d = "";
  for (let s = 0; s <= steps; s++) {
    const x = (s / steps) * width;
    const t = (s / steps) * Math.PI * 2;
    const y =
      mid +
      Math.sin(t * 3 + i * 0.42) * (height * 0.26) +
      Math.sin(t * 7 - i * 0.31) * (height * 0.09) +
      (i - 5) * 1.6;
    d += `${s === 0 ? "M" : "L"}${r2(x)} ${r2(y)}`;
  }
  return d;
}

function rosettePath(ring: number, cx: number, cy: number, radius: number): string {
  const k = 18;
  const steps = 540;
  const a = radius * (0.55 + ring * 0.06);
  const b = radius * 0.18;
  let d = "";
  for (let s = 0; s <= steps; s++) {
    const t = (s / steps) * Math.PI * 2;
    const r = a + b * Math.cos(k * t + ring * (Math.PI / k));
    d += `${s === 0 ? "M" : "L"}${r2(cx + r * Math.cos(t))} ${r2(cy + r * Math.sin(t))}`;
  }
  return `${d}Z`;
}

const W = 480;
const H = 120;
const WAVES = Array.from({ length: 11 }, (_, i) => wavePath(i, W, H));
const ROSETTE = Array.from({ length: 6 }, (_, i) => rosettePath(i, 412, 60, 74));

export function Guilloche({ className, rosette = true }: { className?: string; rosette?: boolean }) {
  return (
    <svg
      viewBox={`0 0 ${W} ${H}`}
      preserveAspectRatio="xMidYMid slice"
      aria-hidden
      focusable="false"
      className={cn("pointer-events-none text-guilloche", className)}
    >
      <g fill="none" stroke="currentColor" strokeWidth={0.6} vectorEffect="non-scaling-stroke">
        {WAVES.map((d, i) => (
          <path key={`w${i}`} d={d} opacity={0.5} />
        ))}
        {rosette &&
          ROSETTE.map((d, i) => <path key={`r${i}`} d={d} opacity={0.65} />)}
      </g>
    </svg>
  );
}
