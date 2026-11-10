"use client";

import type { CSSProperties, ReactNode } from "react";
import { PanelLeft } from "lucide-react";

import { ResizeHandle } from "./ResizeHandle";
import { useMediaQuery } from "@/hooks/useMediaQuery";
import {
  INSPECTOR_MIN_WIDTH, LANE_DIVIDER_WIDTH, PRIMARY_MIN_WIDTH, laneMode, resolvePrimaryLaneWidth,
} from "@/lib/right-panel-width";
import { useT } from "@/components/LocaleProvider";
import styles from "./RightPanelLanes.module.css";

/** The widest the navigator gets when it slides over the preview instead of beside it. */
const OVERLAY_PRIMARY_WIDTH = 300;

/**
 * The panel's layout: one lane at narrow widths, two once there is room.
 *
 * Every mode except the drawer draws the same three children in the same order —
 * the channel lane, the divider, the inspector lane — and only their geometry
 * changes. Moving the channel lane between containers by mode would remount the
 * tree inside it, and the tree's expanded directories and scroll position are
 * exactly what the lane split exists to protect.
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
  /** The left lane's width while both lanes are drawn. */
  primaryWidth: number;
  onResizePrimary: (delta: number) => void;
  onResizePrimaryStart: () => void;
  onResizePrimaryEnd: () => void;
  /** Whether the medium layout has the channel lane sliding over the inspector. */
  navigatorOpen: boolean;
  onToggleNavigator: () => void;
}) {
  const t = useT();
  const drawer = useMediaQuery("(max-width: 960px)");
  const mode = laneMode(props.width, drawer);
  // A width saved on a wider panel is not a width this panel can afford: the
  // split is re-derived from the shell it has now, so the inspector lane can
  // never be squeezed below its floor by a stale number.
  const primaryWidth = resolvePrimaryLaneWidth(props.primaryWidth, props.width);

  // The drawer is a different composition, not a narrower split: one strip with
  // the workspace channels and the open files side by side, as it always was.
  if (mode === "narrow") {
    return (
      <div className={styles.narrow}>
        {props.narrowStrip}
        <div className={styles.body}>{props.narrowBody}</div>
      </div>
    );
  }

  const hasInspector = props.inspectorBody !== null;
  // Too narrow for two columns, wide enough that the tree is still worth having:
  // the navigator slides over the preview instead of squeezing it.
  const overlay = mode === "medium" && hasInspector;
  const parked = overlay && !props.navigatorOpen;

  const primaryStyle: CSSProperties = overlay
    ? { width: `min(${OVERLAY_PRIMARY_WIDTH}px, 78%)` }
    : hasInspector
      ? { width: primaryWidth }
      : { flex: "1 1 auto" };

  const layout = overlay ? "medium" : hasInspector ? "wide" : "single";

  return (
    <div className={styles.lanes} data-mode={layout} data-testid="right-panel-lanes">
      {overlay && props.navigatorOpen && (
        <div
          key="backdrop"
          className={styles.backdrop}
          onPointerDown={props.onToggleNavigator}
          aria-hidden="true"
        />
      )}
      <div
        key="primary"
        className={`${styles.primary}${overlay ? ` ${styles["primary-overlay"]}` : ""}`}
        style={primaryStyle}
        data-open={overlay ? props.navigatorOpen || undefined : undefined}
        data-parked={parked || undefined}
        data-testid="right-panel-primary"
        inert={parked}
      >
        {props.channelStrip}
        <div className={styles.body}>{props.channelBody}</div>
      </div>
      {hasInspector && mode === "wide" && (
        <ResizeHandle
          key="divider"
          side="right"
          ariaLabel={t("rightPanel.resizeNavigator")}
          value={primaryWidth}
          min={PRIMARY_MIN_WIDTH}
          max={Math.max(
            PRIMARY_MIN_WIDTH,
            props.width - LANE_DIVIDER_WIDTH - INSPECTOR_MIN_WIDTH,
          )}
          onResize={props.onResizePrimary}
          onResizeStart={props.onResizePrimaryStart}
          onResizeEnd={props.onResizePrimaryEnd}
        />
      )}
      {hasInspector && (
        <div key="inspector" className={styles.inspector}>
          <div className={styles.strip}>
            {props.inspectorStrip}
            {overlay && (
              // Last, not first: while the navigator covers this lane's left part
              // the control that brings it back has to stay reachable.
              <button
                type="button"
                className={styles["nav-toggle"]}
                aria-label={props.navigatorOpen ? t("rightPanel.hideNavigator") : t("rightPanel.showNavigator")}
                aria-expanded={props.navigatorOpen}
                data-open={props.navigatorOpen || undefined}
                onClick={props.onToggleNavigator}
              >
                <PanelLeft size={15} aria-hidden="true" />
              </button>
            )}
          </div>
          <div className={styles.body}>{props.inspectorBody}</div>
        </div>
      )}
    </div>
  );
}
