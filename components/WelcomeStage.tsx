"use client";

import { useId } from "react";
import { useT } from "@/components/LocaleProvider";
import { BrandMark } from "@/components/BrandMark";

/**
 * Soft blue ellipse behind the centered composer (dsh WelcomeGlow / figma
 * 313:14109). Positioned by `.composer-welcome-glow` on the welcome stack so it
 * sits on the input card, not the headline.
 */
export function WelcomeGlow() {
  const glowFilterId = `empty-glow-${useId().replace(/:/g, "")}`;
  return (
    <svg className="composer-welcome-glow" viewBox="0 0 1051 468" fill="none" aria-hidden="true">
      <defs>
        <filter
          id={glowFilterId}
          x="0"
          y="0"
          width="1051"
          height="468"
          filterUnits="userSpaceOnUse"
          colorInterpolationFilters="sRGB"
        >
          <feFlood floodOpacity="0" result="BackgroundImageFix" />
          <feBlend mode="normal" in="SourceGraphic" in2="BackgroundImageFix" result="shape" />
          <feGaussianBlur stdDeviation="50" result="effect1_foregroundBlur" />
        </filter>
      </defs>
      <g filter={`url(#${glowFilterId})`}>
        <ellipse cx="525.5" cy="234" rx="425.5" ry="134" fill="var(--accent)" fillOpacity="0.08" />
      </g>
    </svg>
  );
}

/**
 * Welcome chrome above the centered composer (dsh WelcomeStage): brand mark +
 * headline + preview badge. The workspace chip and input card sit below
 * this row, not in the transcript.
 */
export function WelcomeStage(props: {
  hasModel: boolean;
  onOpenSettings: () => void;
}) {
  const { hasModel, onOpenSettings } = props;
  const t = useT();

  return (
    <div className="welcome-stage" data-testid="welcome-stage">
      <div className="welcome-stage-headline">
        <span className="welcome-stage-mark" aria-hidden="true"><BrandMark size={28} /></span>
        <h1 className="welcome-stage-title">{t("welcome.headline")}</h1>
        <span className="welcome-stage-preview">{t("welcome.preview")}</span>
        {!hasModel && (
          <button type="button" className="welcome-stage-settings" onClick={() => onOpenSettings()}>
            {t("empty.noModel")}
          </button>
        )}
      </div>
    </div>
  );
}
