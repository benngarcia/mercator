import { clsx, type ClassValue } from "clsx";
import { twMerge } from "tailwind-merge";

/**
 * cn merges conditional class names (clsx) and de-duplicates conflicting
 * Tailwind utilities (tailwind-merge). The canonical shadcn/ui helper.
 */
export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
