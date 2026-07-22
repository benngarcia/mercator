import { flushSync } from "react-dom";
import * as Context from "effect/Context";
import * as Effect from "effect/Effect";
import * as Layer from "effect/Layer";

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
    "startViewTransition" in document &&
    !window.matchMedia("(prefers-reduced-motion: reduce)").matches
  );
}

export const layer = Layer.effect(
  CanvasTransition,
  Effect.gen(function* () {
    const scope = yield* Effect.scope;
    let active: ViewTransition | null = null;

    const commit = Effect.fn("CanvasTransition.commit")(function* (
      animate: boolean,
      update: () => void,
    ) {
      if (!animate || !supportsViewTransitions() || active !== null) {
        yield* Effect.sync(update);
        return;
      }
      const transition = document.startViewTransition(() => flushSync(update));
      active = transition;
      yield* Effect.tryPromise({
        try: () => transition.updateCallbackDone,
        catch: () => undefined,
      }).pipe(Effect.ignore);
      yield* Effect.forkIn(
        Effect.tryPromise({
          try: () => transition.finished,
          catch: () => undefined,
        }).pipe(
          Effect.ignore,
          Effect.ensuring(
            Effect.sync(() => {
              if (active === transition) {
                active = null;
              }
            }),
          ),
        ),
        scope,
      );
    });

    return CanvasTransition.of({ commit });
  }),
);
