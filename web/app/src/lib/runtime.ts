import * as Layer from "effect/Layer";
import * as Atom from "effect/unstable/reactivity/Atom";
import * as BrowserHttpClient from "@effect/platform-browser/BrowserHttpClient";

import * as Api from "./api/client";
import * as Session from "./session";
import * as DeploymentEvents from "./deployment/feed";
import * as CanvasTransition from "./deployment/transition";

const apiLayer = Api.layer.pipe(Layer.provide(Session.layer));
const deploymentEventsLayer = DeploymentEvents.layer.pipe(
  Layer.provide(Layer.merge(Session.layer, BrowserHttpClient.layerFetch)),
);

export const liveLayer = Layer.mergeAll(
  Session.layer,
  apiLayer,
  deploymentEventsLayer,
  CanvasTransition.layer,
);

export const runtime = Atom.runtime(liveLayer);
