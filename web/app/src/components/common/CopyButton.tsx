import * as React from "react";
import { Check, Copy } from "lucide-react";

import { cn } from "@/lib/utils";
import { Button } from "@/components/ui/button";
import {
  Tooltip,
  TooltipContent,
  TooltipTrigger,
} from "@/components/ui/tooltip";
import { useMountEffect } from "@/hooks/useMountEffect";

function CopiedReset({ reset }: { reset: () => void }) {
  useMountEffect(() => {
    const timeout = setTimeout(reset, 1200);
    return () => clearTimeout(timeout);
  });
  return null;
}

export interface CopyButtonProps {
  value: string;
  /** Optional accessible label / tooltip text. Defaults to "Copy". */
  label?: string;
  className?: string;
  size?: "icon" | "inline";
}

/**
 * CopyButton copies `value` to the clipboard and flips to a check for a beat.
 * The "inline" size is a tiny affordance meant to sit next to mono ids/digests
 * in dense tables; "icon" is the standard square button.
 */
export function CopyButton({
  value,
  label = "Copy",
  className,
  size = "inline",
}: CopyButtonProps) {
  const [copied, setCopied] = React.useState(false);
  const [copiedVersion, setCopiedVersion] = React.useState(0);

  const onCopy = React.useCallback(
    async (event: React.MouseEvent) => {
      event.stopPropagation();
      try {
        await navigator.clipboard.writeText(value);
        setCopied(true);
        setCopiedVersion((version) => version + 1);
      } catch {
        // Clipboard can be unavailable (insecure context); fail quietly.
      }
    },
    [value],
  );

  const Icon = copied ? Check : Copy;

  return (
    <>
      {copied && <CopiedReset key={copiedVersion} reset={() => setCopied(false)} />}
      <Tooltip>
        <TooltipTrigger asChild>
          <Button
            type="button"
            variant="ghost"
            onClick={onCopy}
            aria-label={label}
            className={cn(
              size === "inline"
                ? "size-6 rounded p-0 text-muted-foreground hover:text-foreground [&_svg]:size-3.5"
                : "size-8 p-0 text-muted-foreground hover:text-foreground",
              className,
            )}
          >
            <Icon className={cn(copied && "text-phase-succeeded")} />
          </Button>
        </TooltipTrigger>
        <TooltipContent>{copied ? "Copied" : label}</TooltipContent>
      </Tooltip>
    </>
  );
}
