import * as Effect from "effect/Effect";
import * as Schema from "effect/Schema";
import { describe, expect, it } from "vitest";

import { CloudEvent, OfferCatalogReplacement } from "./contracts";
import labConsoleFeed from "./testdata/lab-console-feed.json";

// The console decodes every frame of the workspace feed against a hand-written
// schema, and a frame it refuses ends the stream. Nothing then reaches the
// reducer, `ready` never arrives, and the canvas draws its skeleton forever with
// no console error, no uncaught exception, and no failed request. The only
// symptom is a page that never renders, which is why this gap cost a full CI
// cycle to find: the browser proof reported a missing heading.
//
// The fixture is the real feed, captured from the Lab server driving the demo
// Blueprint the browser proof drives. Its first frame carries an offer with no
// `reliability` key at all, which is what a machine whose publisher has measured
// nothing looks like on the wire, and which the schema used to require.
//
// This runs in `bun run test` rather than behind a browser on purpose. What
// broke is a disagreement between two documents about one payload, and proving
// it needs the payload and the schema, not a rendered page.
describe("the workspace feed the Lab actually serves", () => {
  const schemas = {
    domain_event: CloudEvent,
    offers_replaced: OfferCatalogReplacement,
  } as const;

  const decodable = labConsoleFeed.filter(
    (frame): frame is (typeof labConsoleFeed)[number] & { event: keyof typeof schemas } =>
      frame.event in schemas,
  );

  it("carries a machine whose publisher measured nothing", () => {
    const replacement = labConsoleFeed.find(
      (frame) => frame.event === "offers_replaced",
    );
    const offers = (replacement?.data as { offers?: unknown[] }).offers ?? [];
    expect(offers.length).toBeGreaterThan(0);
    expect(offers.some((offer) => !("reliability" in (offer as object)))).toBe(
      true,
    );
  });

  it.each(decodable.map((frame, index) => [index, frame] as const))(
    "decodes frame %i",
    (_index, frame) => {
      const exit = Effect.runSyncExit(
        Schema.decodeUnknownEffect(schemas[frame.event])(frame.data),
      );
      expect(exit._tag, JSON.stringify(exit, null, 2).slice(0, 2000)).toBe(
        "Success",
      );
    },
  );
});
