import * as Clock from "effect/Clock";
import * as Duration from "effect/Duration";
import * as Schedule from "effect/Schedule";
import * as Stream from "effect/Stream";

export function clock(intervalMs: number): Stream.Stream<number> {
  return Stream.fromEffect(Clock.currentTimeMillis).pipe(
    Stream.concat(
      Stream.fromSchedule(Schedule.spaced(Duration.millis(intervalMs))).pipe(
        Stream.mapEffect(() => Clock.currentTimeMillis),
      ),
    ),
  );
}
