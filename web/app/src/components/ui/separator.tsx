import * as React from "react";

import { cn } from "@/lib/utils";

type SeparatorProps = React.ComponentPropsWithoutRef<"hr"> & {
  decorative?: boolean;
  orientation?: "horizontal" | "vertical";
};

const Separator = React.forwardRef<HTMLHRElement, SeparatorProps>(
  (
    { className, orientation = "horizontal", decorative = true, ...props },
    ref,
  ) => (
    <hr
      ref={ref}
      role={decorative ? "none" : undefined}
      aria-orientation={decorative ? undefined : orientation}
      className={cn(
        "shrink-0 border-0 bg-border",
        orientation === "horizontal" ? "h-px w-full" : "h-full w-px",
        className,
      )}
      {...props}
    />
  ),
);
Separator.displayName = "Separator";

export { Separator };
