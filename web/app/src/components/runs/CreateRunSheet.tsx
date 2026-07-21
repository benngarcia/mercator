import * as React from "react";

import {
  Sheet,
  SheetContent,
  SheetDescription,
  SheetHeader,
  SheetTitle,
} from "@/components/ui/sheet";
import { Tabs, TabsList, TabsTrigger } from "@/components/ui/tabs";
import { CreateRunForm } from "./CreateRunForm";

type Mode = "image" | "spec";

export interface CreateRunSheetProps {
  open: boolean;
  onDismiss: () => void;
  onCreated: (runId: string) => void;
  returnFocusRef: React.RefObject<HTMLElement | null>;
}

/**
 * CreateRunSheet owns the transient run-intake workflow: mode selection,
 * pending-dismissal state, and accessible overlay behavior. Runs owns the
 * surrounding URL and history transitions.
 */
export function CreateRunSheet({
  open,
  onDismiss,
  onCreated,
  returnFocusRef,
}: CreateRunSheetProps) {
  const [mode, setMode] = React.useState<Mode>("image");
  const [pending, setPending] = React.useState(false);

  const dismiss = () => {
    if (pending) return;
    setMode("image");
    onDismiss();
  };

  return (
    <Sheet open={open} onOpenChange={(nextOpen) => !nextOpen && dismiss()}>
      <SheetContent
        className="w-full overflow-y-auto sm:max-w-2xl"
        closeDisabled={pending}
        onCloseAutoFocus={(event) => {
          const trigger = returnFocusRef.current;
          if (!trigger) return;
          event.preventDefault();
          trigger.focus();
        }}
      >
        <SheetHeader className="pr-8">
          <SheetTitle>Create run</SheetTitle>
          <SheetDescription>
            Place a workload on an offer with an image shorthand or a full
            immutable workload specification.
          </SheetDescription>
        </SheetHeader>

        <Tabs value={mode} onValueChange={(value) => setMode(value as Mode)}>
          <TabsList aria-label="Run input">
            <TabsTrigger value="image" disabled={pending}>
              Image
            </TabsTrigger>
            <TabsTrigger value="spec" disabled={pending}>
              Spec
            </TabsTrigger>
          </TabsList>
        </Tabs>

        <CreateRunForm
          mode={mode}
          onCreated={onCreated}
          onPendingChange={setPending}
        />
      </SheetContent>
    </Sheet>
  );
}
