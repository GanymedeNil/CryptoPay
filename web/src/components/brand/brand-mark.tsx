import { cn } from "@/lib/utils";

const r2 = (n: number) => Math.round(n * 100) / 100;

function ring(i: number): string {
  const steps = 180;
  let d = "";
  for (let s = 0; s <= steps; s++) {
    const t = (s / steps) * Math.PI * 2;
    const r = 6.2 + i * 0.9 + 2.2 * Math.cos(8 * t + i * 0.4);
    d += `${s === 0 ? "M" : "L"}${r2(12 + r * Math.cos(t))} ${r2(12 + r * Math.sin(t))}`;
  }
  return `${d}Z`;
}

const RINGS = [0, 1, 2].map(ring);

/** Small rosette + wordmark. */
export function BrandMark({ className, label = "CryptoPay" }: { className?: string; label?: string }) {
  return (
    <span className={cn("inline-flex items-center gap-2 font-heading text-[15px] font-semibold tracking-tight", className)}>
      <svg viewBox="0 0 24 24" className="size-5 text-primary" aria-hidden focusable="false">
        <g fill="none" stroke="currentColor" strokeWidth={0.9}>
          {RINGS.map((d, i) => (
            <path key={i} d={d} opacity={1 - i * 0.25} />
          ))}
        </g>
      </svg>
      {label}
    </span>
  );
}
