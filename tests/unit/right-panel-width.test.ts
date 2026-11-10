import { describe, expect, it } from "vitest";

import {
  CHAT_MIN_WIDTH,
  DUAL_LANE_MIN_WIDTH,
  INSPECTOR_MIN_WIDTH,
  LANE_DIVIDER_WIDTH,
  PRIMARY_MIN_WIDTH,
  RIGHT_DEFAULT_WIDTH,
  RIGHT_MIN_WIDTH,
  laneMode,
  resolvePrimaryLaneWidth,
  resolveRightPanelWidth,
  rightPanelMaxWidth,
} from "@/lib/right-panel-width";

describe("right panel width", () => {
  it("caps the panel so the conversation keeps its readable minimum", () => {
    // 1440 - 280 sidebar - 5 handle - 440 chat
    expect(rightPanelMaxWidth(1440, 280)).toBe(715);
    expect(rightPanelMaxWidth(1280, 280)).toBe(555);
    expect(rightPanelMaxWidth(1920, 280)).toBe(1000); // RIGHT_MAX_WIDTH wins over 1185
  });

  it("never goes below the single-lane floor", () => {
    expect(rightPanelMaxWidth(900, 420)).toBe(RIGHT_MIN_WIDTH);
  });

  it("treats a collapsed sidebar as zero width", () => {
    expect(rightPanelMaxWidth(1440, 0)).toBe(995);
  });

  it("defaults a fresh profile to a width that can hold two lanes", () => {
    expect(resolveRightPanelWidth(null, 1440, 280)).toBe(RIGHT_DEFAULT_WIDTH);
    expect(resolveRightPanelWidth(null, 1440, 280)).toBeGreaterThanOrEqual(DUAL_LANE_MIN_WIDTH);
    // On a 1280 viewport the room caps the default instead of the default capping the room.
    expect(resolveRightPanelWidth(null, 1280, 280)).toBe(555);
  });

  it("does not trust a stored width that the current viewport cannot afford", () => {
    expect(resolveRightPanelWidth(1000, 1440, 280)).toBe(715);
    expect(resolveRightPanelWidth(680, 1280, 280)).toBe(555);
    expect(resolveRightPanelWidth(50, 1440, 280)).toBe(RIGHT_MIN_WIDTH);
    expect(resolveRightPanelWidth(Number.NaN, 1440, 280)).toBe(RIGHT_DEFAULT_WIDTH);
  });
});

describe("primary lane width", () => {
  it("defaults to 280 when nothing was stored", () => {
    expect(resolvePrimaryLaneWidth(null, 680)).toBe(280);
  });

  it("keeps the stored width while the inspector lane still fits", () => {
    expect(resolvePrimaryLaneWidth(400, 680)).toBe(400);
  });

  it("stops the primary lane from starving the inspector lane", () => {
    // 680 - 8 divider - 264 inspector
    expect(resolvePrimaryLaneWidth(999, 680)).toBe(408);
    // 500 - 8 - 264
    expect(resolvePrimaryLaneWidth(300, 500)).toBe(228);
  });

  it("holds the primary floor when the shell cannot fit both lanes", () => {
    expect(resolvePrimaryLaneWidth(100, 680)).toBe(PRIMARY_MIN_WIDTH);
    expect(resolvePrimaryLaneWidth(300, 400)).toBe(PRIMARY_MIN_WIDTH);
  });
});

describe("lane mode", () => {
  it("draws both lanes once the shell is wide enough", () => {
    expect(laneMode(680, false)).toBe("wide");
    expect(laneMode(DUAL_LANE_MIN_WIDTH, false)).toBe("wide");
  });

  it("drops the primary lane to an overlay between the two floors", () => {
    expect(laneMode(DUAL_LANE_MIN_WIDTH - 1, false)).toBe("medium");
    expect(laneMode(RIGHT_MIN_WIDTH, false)).toBe("medium");
  });

  it("falls back to the single tab strip below the floor", () => {
    expect(laneMode(RIGHT_MIN_WIDTH - 1, false)).toBe("narrow");
  });

  it("is always narrow in the drawer, whatever the width", () => {
    expect(laneMode(700, true)).toBe("narrow");
    expect(laneMode(1400, true)).toBe("narrow");
  });
});

describe("constants", () => {
  it("keeps the lane floors inside the dual-lane threshold", () => {
    expect(DUAL_LANE_MIN_WIDTH).toBeGreaterThanOrEqual(
      PRIMARY_MIN_WIDTH + INSPECTOR_MIN_WIDTH + LANE_DIVIDER_WIDTH,
    );
    expect(CHAT_MIN_WIDTH).toBeGreaterThanOrEqual(320);
    expect(RIGHT_DEFAULT_WIDTH).toBeGreaterThanOrEqual(DUAL_LANE_MIN_WIDTH);
  });
});
