// Pixel icon set for the honey-garden console, generated from the design spec
// (docs/claude/design/pixel-icons.json via web/src/hive/pixel-icons.ts).
//
// Every icon is a 16x16 monochrome grid painted with currentColor, so it follows
// the text colour of whatever it sits in. Sizes are limited to 16/32/48/64px;
// anything else warns in development.
//
// The names are the ones the console already used, minus "hexagon" (the spec has
// no equivalent) and plus "bee", which is the logo / bee marker.
import { ICON_GRIDS } from "./pixel-icons";
import { assertPixelSize, PixelArt } from "./pixel";

export type IconName =
  | "hive"
  | "swarm"
  | "chat"
  | "timeline"
  | "inbox"
  | "memory"
  | "settings"
  | "shield"
  | "branch"
  | "pr"
  | "diff"
  | "terminal"
  | "clock"
  | "check"
  | "alert"
  | "close"
  | "plus"
  | "search"
  | "filter"
  | "send"
  | "back"
  | "chevron"
  | "external"
  | "user"
  | "refresh"
  | "pause"
  | "play"
  | "crown"
  | "bee";

/** The size steps that keep a 16x16 grid on whole pixels. */
export const ICON_STEPS = [16, 32, 48, 64] as const;

export function Icon({
  name,
  size = 16,
  className,
}: {
  name: IconName;
  size?: number;
  className?: string;
}) {
  assertPixelSize(`icon:${name}`, size);
  return (
    <PixelArt
      rows={ICON_GRIDS[name]}
      size={size}
      className={className ? `hg-icon ${className}` : "hg-icon"}
    />
  );
}

export function GithubMark({ size = 16 }: { size?: number }) {
  return (
    <svg
      className="hg-icon"
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="currentColor"
      aria-hidden="true"
      focusable="false"
    >
      <path d="M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82a7.42 7.42 0 0 1 2-.27c.68 0 1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38A8.01 8.01 0 0 0 16 8c0-4.42-3.58-8-8-8Z" />
    </svg>
  );
}
