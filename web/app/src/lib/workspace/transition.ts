import { flushSync } from "react-dom";
import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";
import * as Semaphore from "effect/Semaphore";

export interface CanvasTransitionService {
  readonly commit: (
    animate: boolean,
    update: () => void,
  ) => Effect.Effect<void>;
}

export class CanvasTransition extends Context.Service<
  CanvasTransition,
  CanvasTransitionService
>()("@mercator/CanvasTransition") {}

function supportsViewTransitions(): boolean {
  return (
    typeof document !== "undefined" &&
    document.visibilityState === "visible" &&
    "startViewTransition" in document &&
    !window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

const awaitTransition = (transition: ViewTransition) => {
  const settled = Promise.all([
    transition.ready.catch(() => undefined),
    transition.updateCallbackDone.catch(() => undefined),
    transition.finished.catch(() => undefined),
  ]);
  return Effect.promise(() => settled).pipe(Effect.asVoid);
};

function startCanvasTransition(update: () => void): ViewTransition | null {
  try {
    return document.startViewTransition(() => flushSync(update));
  } catch {
    flushSync(update);
    return null;
  }
}

export const layer = Layer.effect(
  CanvasTransition,
  Effect.gen(function* () {
    const animation = yield* Semaphore.make(1);

    const commit = Effect.fn("CanvasTransition.commit")((
      animate: boolean,
      update: () => void,
    ) =>
      animation.withPermit(
        Effect.gen(function* () {
          if (!animate || !supportsViewTransitions()) {
            yield* Effect.sync(update);
            return;
          }
          const transition = startCanvasTransition(update);
          if (transition !== null) yield* awaitTransition(transition);
        }),
      ),
    );

    return CanvasTransition.of({ commit });
  }),
);
