"use client";

import type { ReactNode } from "react";

import { ResizeHandle } from "./ResizeHandle";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import { INSPECTOR_MIN_WIDTH, LANE_DIVIDER_WIDTH, PRIMARY_MIN_WIDTH, laneMode } from "@/lib/right-panel-width";
import styles from "./RightPanelLanes.module.css";

/**
 * The panel's layout: one lane at narrow widths, two once there is room.
 *
 * Both lanes stay mounted when both are drawn. That is the whole point of the
 * split — a file opening in the inspector lane must not unmount the tree, or the
 * user loses the directory they were browsing and the tree loses its scroll
 * position. The divider only changes widths; it never decides what renders.
 *
 * Which mode applies is `laneMode`'s decision, from the panel's own width and
 * the drawer breakpoint. The drawer fills the viewport and has no conversation
 * beside it to protect, so it is always the single strip.
 */
export function RightPanelLanes(props: {
  /** The panel's current width, both lanes and the divider included. */
  width: number;
  /** Left lane: the channel tab strip and the selected channel's body. */
  channelStrip: ReactNode;
  channelBody: ReactNode;
  /** Right lane: the open files. `null` when no file is open. */
  inspectorStrip: ReactNode | null;
  inspectorBody: ReactNode | null;
  /** The single strip and body the narrow layout draws, strips already merged. */
  narrowStrip: ReactNode;
  narrowBody: ReactNode;
  /** The left lane's width. */
  primaryWidth: number;
  onResizePrimary: (delta: number) => void;
}) {
  const drawer = useMediaQuery("(max-width: 960px)");
  const mode = laneMode(props.width, drawer);

  // Both lanes need their floors before they are worth drawing; below that the
  // panel is the strip it always was.
  if (mode !== "wide") {
    return (
      <div className={styles.narrow}>
        {props.narrowStrip}
        <div className={styles.body}>{props.narrowBody}</div>
      </div>
    );
  }

  // With nothing open on the right there is no split to draw: the channel lane
  // takes the whole panel, exactly as it did before the lanes existed.
  const hasInspector = props.inspectorBody !== null;

  return (
    <div className={styles.lanes} data-mode={mode} data-testid="right-panel-lanes">
      <div
        className={styles.primary}
        style={hasInspector ? { width: props.primaryWidth } : { flex: "1 1 auto" }}
      >
        {props.channelStrip}
        <div className={styles.body}>{props.channelBody}</div>
      </div>
      {hasInspector && (
        <>
          <ResizeHandle
            side="right"
            ariaLabel="Resize the file navigator"
            value={props.primaryWidth}
            min={PRIMARY_MIN_WIDTH}
            max={Math.max(
              PRIMARY_MIN_WIDTH,
              props.width - LANE_DIVIDER_WIDTH - INSPECTOR_MIN_WIDTH,
            )}
            onResize={props.onResizePrimary}
          />
          <div className={styles.inspector}>
            {props.inspectorStrip}
            <div className={styles.body}>{props.inspectorBody}</div>
          </div>
        </>
      )}
    </div>
  );
}
