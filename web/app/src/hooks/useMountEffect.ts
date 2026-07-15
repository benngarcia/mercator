import { useEffect } from "react";

export function useMountEffect(setup: () => void | (() => void)) {
  useEffect(setup, []);
}
