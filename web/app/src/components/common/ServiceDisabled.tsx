import { PowerOff } from "lucide-react";

import { cn } from "@/lib/utils";
import { Alert, AlertDescription, AlertTitle } from "@/components/ui/alert";

export interface ServiceDisabledProps {
  /**
   * Human name of the feature the server reported disabled, e.g. "Workloads",
   * "Sinks", "Image resolver". Surfaced for 501 responses.
   */
  feature: string;
  className?: string;
}

/**
 * ServiceDisabled is the soft-degrade panel rendered when the Go server
 * returns 501 (WORKLOAD_SERVICE_DISABLED / SINKS_DISABLED /
 * IMAGE_RESOLVER_DISABLED). It is informational, not an error: the operator
 * simply hasn't enabled that subsystem on this deployment.
 */
export function ServiceDisabled({ feature, className }: ServiceDisabledProps) {
  return (
    <Alert className={cn("border-dashed", className)}>
      <PowerOff />
      <AlertTitle>{feature} is not enabled</AlertTitle>
      <AlertDescription>
        This deployment has the {feature.toLowerCase()} subsystem turned off.
        Enable it in the Mercator server configuration to use this view.
      </AlertDescription>
    </Alert>
  );
}
