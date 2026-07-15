import { useSyncExternalStore } from "react";
import { Toaster as Sonner, type ToasterProps } from "sonner";

/**
 * Toaster wired to the app theme. The console toggles a `.dark` class on the
 * document element (see ThemeToggle), so we observe that rather than depending
 * on a theme provider package.
 */
function documentTheme(): "light" | "dark" {
  return typeof document !== "undefined" && document.documentElement.classList.contains("dark")
    ? "dark"
    : "light";
}

function subscribeToDocumentTheme(notify: () => void) {
  const el = document.documentElement;
  const observer = new MutationObserver(notify);
  observer.observe(el, { attributes: true, attributeFilter: ["class"] });
  return () => observer.disconnect();
}

function useDocumentTheme(): "light" | "dark" {
  return useSyncExternalStore(subscribeToDocumentTheme, documentTheme, () => "light");
}

function Toaster(props: ToasterProps) {
  const theme = useDocumentTheme();

  return (
    <Sonner
      theme={theme}
      className="toaster group"
      style={
        {
          "--normal-bg": "var(--popover)",
          "--normal-text": "var(--popover-foreground)",
          "--normal-border": "var(--border)",
        } as React.CSSProperties
      }
      {...props}
    />
  );
}

export { Toaster };
