"use client";

import { useId } from "react";
import { useT } from "@/components/LocaleProvider";

/**
 * Soft blue ellipse behind the centered composer (dsh HeroGlow / figma
 * 313:14109). Positioned by `.composer-hero-glow` on the hero stack so it
 * sits on the input card, not the headline.
 */
export function HeroGlow() {
  const glowFilterId = `empty-glow-${useId().replace(/:/g, "")}`;
  return (
    <svg className="composer-hero-glow" viewBox="0 0 1051 468" fill="none" aria-hidden="true">
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
        <ellipse cx="525.5" cy="234" rx="425.5" ry="134" fill="#6187D8" fillOpacity="0.08" />
      </g>
    </svg>
  );
}

/**
 * Hero chrome above the centered composer (dsh HeroShell): brand mark +
 * headline + preview badge. The workspace chip and input card sit below
 * this row, not in the transcript.
 */
export function EmptyHero(props: {
  hasModel: boolean;
  onOpenSettings: () => void;
}) {
  const { hasModel, onOpenSettings } = props;
  const t = useT();

  return (
    <div className="empty-hero" data-testid="empty-hero">
      <div className="empty-hero-headline">
        <span className="empty-hero-mark" aria-hidden="true">E</span>
        <h1 className="empty-hero-title">{t("hero.headline")}</h1>
        <span className="empty-hero-preview">{t("hero.preview")}</span>
        {!hasModel && (
          <button type="button" className="empty-hero-settings" onClick={() => onOpenSettings()}>
            {t("empty.noModel")}
          </button>
        )}
      </div>
    </div>
  );
}
