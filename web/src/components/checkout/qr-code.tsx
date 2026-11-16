import { useMemo } from "react";
import { encode } from "uqr";
import { cn } from "@/lib/utils";

/**
 * QR code rendered as a single SVG path (dark modules on a light tile, also
 * in dark mode, so every scanner can read it).
 */
export function QrCode({ value, label, className }: { value: string; label: string; className?: string }) {
  const { path, size } = useMemo(() => {
    const qr = encode(value, { ecc: "M", border: 2 });
    let d = "";
    for (let y = 0; y < qr.size; y++) {
      let x = 0;
      while (x < qr.size) {
        if (!qr.data[y][x]) {
          x++;
          continue;
        }
        const start = x;
        while (x < qr.size && qr.data[y][x]) x++;
        d += `M${start} ${y}h${x - start}v1h${start - x}z`;
      }
    }
    return { path: d, size: qr.size };
  }, [value]);

  return (
    <svg
      role="img"
      aria-label={label}
      viewBox={`0 0 ${size} ${size}`}
      shapeRendering="crispEdges"
      className={cn("block rounded-lg bg-white", className)}
    >
      <path d={path} fill="#0b1411" />
    </svg>
  );
}
