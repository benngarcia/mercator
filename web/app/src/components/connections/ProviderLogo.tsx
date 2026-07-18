import { cn } from "@/lib/utils";

// Bundled provider logomarks, keyed by the manifest's `logo` slug. The console
// runs under a CSP that forbids external image fetches, so every mark ships in
// the bundle. Marks we cannot bundle cleanly (no suitably licensed asset) fall
// back to a typographic monogram; see LOGO-LICENSES.md next to this file.
//
// Each entry is an SVG path on a 24×24 viewBox drawn with currentColor.
const LOGOMARK_PATHS: Record<string, string> = {
  // Docker whale, from the Simple Icons collection (CC0-1.0).
  docker:
    "M13.983 11.078h2.119a.186.186 0 00.186-.185V9.006a.186.186 0 00-.186-.186h-2.119a.185.185 0 00-.185.185v1.888c0 .102.083.185.185.185m-2.954-5.43h2.118a.186.186 0 00.186-.186V3.574a.186.186 0 00-.186-.185h-2.118a.185.185 0 00-.185.185v1.888c0 .102.082.185.185.185m0 2.716h2.118a.187.187 0 00.186-.186V6.29a.186.186 0 00-.186-.185h-2.118a.185.185 0 00-.185.185v1.887c0 .102.082.185.185.186m-2.93 0h2.12a.186.186 0 00.184-.186V6.29a.185.185 0 00-.185-.185H8.1a.185.185 0 00-.185.185v1.887c0 .102.083.185.185.186m-2.964 0h2.119a.186.186 0 00.185-.186V6.29a.185.185 0 00-.185-.185H5.136a.186.186 0 00-.186.185v1.887c0 .102.084.185.186.186m5.893 2.715h2.118a.186.186 0 00.186-.185V9.006a.186.186 0 00-.186-.186h-2.118a.185.185 0 00-.185.185v1.888c0 .102.082.185.185.185m-2.93 0h2.12a.185.185 0 00.184-.185V9.006a.185.185 0 00-.184-.186h-2.12a.185.185 0 00-.184.185v1.888c0 .102.083.185.185.185m-2.964 0h2.119a.185.185 0 00.185-.185V9.006a.185.185 0 00-.184-.186h-2.12a.186.186 0 00-.186.186v1.887c0 .102.084.185.186.185m-2.92 0h2.12a.185.185 0 00.184-.185V9.006a.185.185 0 00-.184-.186h-2.12a.185.185 0 00-.184.185v1.888c0 .102.082.185.185.185M23.763 9.89c-.065-.051-.672-.51-1.954-.51-.338.001-.676.03-1.01.087-.248-1.7-1.653-2.53-1.716-2.566l-.344-.199-.226.327c-.284.438-.49.922-.612 1.43-.23.97-.09 1.882.403 2.661-.595.332-1.55.413-1.744.42H.751a.751.751 0 00-.75.748 11.376 11.376 0 00.692 4.062c.545 1.428 1.355 2.48 2.41 3.124 1.18.723 3.1 1.137 5.275 1.137.983.003 1.963-.086 2.93-.266a12.248 12.248 0 003.823-1.389c.98-.567 1.86-1.288 2.61-2.136 1.252-1.418 1.998-2.997 2.553-4.4h.221c1.372 0 2.215-.549 2.68-1.009.309-.293.55-.65.707-1.046l.098-.288Z",
};

// Monogram letters for slugs without a bundled mark. Derived from the display
// name at the call site when the slug is entirely unknown.
const MONOGRAMS: Record<string, string> = {
  runpod: "R",
  shadeform: "S",
  vast: "V",
};

export interface ProviderLogoProps {
  /** Manifest logo slug (e.g. "docker"). */
  slug: string;
  /** Display name, used to derive a monogram for unknown slugs. */
  name: string;
  className?: string;
}

/**
 * ProviderLogo renders the provider's bundled logomark, or a typographic
 * monogram in the same container treatment when no mark is bundled. The
 * container is a quiet rounded tile that reads identically across both forms.
 */
export function ProviderLogo({ slug, name, className }: ProviderLogoProps) {
  const path = LOGOMARK_PATHS[slug];
  const monogram = MONOGRAMS[slug] ?? name.charAt(0).toUpperCase() ?? "?";

  return (
    <div
      aria-hidden
      className={cn(
        "flex size-10 shrink-0 items-center justify-center rounded-lg border border-border bg-muted/40 text-foreground",
        className,
      )}
    >
      {path ? (
        <svg viewBox="0 0 24 24" className="size-5 fill-current">
          <path d={path} />
        </svg>
      ) : (
        <span className="font-mono text-base font-semibold tracking-tight">
          {monogram}
        </span>
      )}
    </div>
  );
}
