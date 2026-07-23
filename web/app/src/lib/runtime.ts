import * as Layer from "effect/Layer";
import * as Atom from "effect/unstable/reactivity/Atom";
import * as BrowserHttpClient from "@effect/platform-browser/BrowserHttpClient";

import * as Api from "./api/client";
import * as Session from "./session";
import * as WorkspaceEvents from "./workspace/feed";
import * as CanvasTransition from "./workspace/transition";

const apiLayer = Api.layer.pipe(Layer.provide(Session.layer));
const workspaceEventsLayer = WorkspaceEvents.layer.pipe(
  Layer.provide(Layer.merge(Session.layer, BrowserHttpClient.layerFetch)),
);

export const liveLayer = Layer.mergeAll(
  Session.layer,
  apiLayer,
  workspaceEventsLayer,
  CanvasTransition.layer,
);

export const runtime = Atom.runtime(liveLayer);
