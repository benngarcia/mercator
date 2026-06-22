import * as React from "react";
import * as LabelPrimitive from "@radix-ui/react-label";

import { cn } from "@/lib/utils";
import { Label } from "@/components/ui/label";

/**
 * Lightweight, dependency-free form primitives.
 *
 * The canonical shadcn `form` component is built on react-hook-form, which is
 * NOT a declared dependency of this project. Per the design spec we prefer a
 * lightweight controlled form to avoid new deps, so these primitives are plain
 * layout + accessibility helpers (label association, aria-invalid, error text)
 * that a controlled form can drive directly. There is no resolver/registration
 * machinery — wire `value`/`onChange` and pass any error string to
 * <FormMessage>.
 */

interface FormItemContextValue {
  id: string;
}

const FormItemContext = React.createContext<FormItemContextValue | null>(null);

function useFormItem(): FormItemContextValue {
  const ctx = React.useContext(FormItemContext);
  if (!ctx) {
    throw new Error("Form field components must be used within <FormItem>");
  }
  return ctx;
}

const Form = React.forwardRef<
  HTMLFormElement,
  React.FormHTMLAttributes<HTMLFormElement>
>(({ className, ...props }, ref) => (
  <form ref={ref} className={cn("space-y-6", className)} {...props} />
));
Form.displayName = "Form";

const FormItem = React.forwardRef<
  HTMLDivElement,
  React.HTMLAttributes<HTMLDivElement>
>(({ className, ...props }, ref) => {
  const id = React.useId();
  return (
    <FormItemContext.Provider value={{ id }}>
      <div ref={ref} className={cn("space-y-2", className)} {...props} />
    </FormItemContext.Provider>
  );
});
FormItem.displayName = "FormItem";

const FormLabel = React.forwardRef<
  React.ElementRef<typeof LabelPrimitive.Root>,
  React.ComponentPropsWithoutRef<typeof LabelPrimitive.Root> & {
    error?: boolean;
  }
>(({ className, error, ...props }, ref) => {
  const { id } = useFormItem();
  return (
    <Label
      ref={ref}
      htmlFor={id}
      className={cn(error && "text-destructive", className)}
      {...props}
    />
  );
});
FormLabel.displayName = "FormLabel";

const FormControl = React.forwardRef<
  HTMLElement,
  { children: React.ReactElement; error?: boolean }
>(({ children, error }, _ref) => {
  const { id } = useFormItem();
  return React.cloneElement(
    children as React.ReactElement<Record<string, unknown>>,
    {
      id,
      "aria-invalid": error ? true : undefined,
      "aria-describedby": error ? `${id}-message` : undefined,
    },
  );
});
FormControl.displayName = "FormControl";

const FormDescription = React.forwardRef<
  HTMLParagraphElement,
  React.HTMLAttributes<HTMLParagraphElement>
>(({ className, ...props }, ref) => (
  <p
    ref={ref}
    className={cn("text-sm text-muted-foreground", className)}
    {...props}
  />
));
FormDescription.displayName = "FormDescription";

const FormMessage = React.forwardRef<
  HTMLParagraphElement,
  React.HTMLAttributes<HTMLParagraphElement>
>(({ className, children, ...props }, ref) => {
  const { id } = useFormItem();
  if (!children) return null;
  return (
    <p
      ref={ref}
      id={`${id}-message`}
      className={cn("text-sm font-medium text-destructive", className)}
      {...props}
    >
      {children}
    </p>
  );
});
FormMessage.displayName = "FormMessage";

export {
  Form,
  FormItem,
  FormLabel,
  FormControl,
  FormDescription,
  FormMessage,
};
