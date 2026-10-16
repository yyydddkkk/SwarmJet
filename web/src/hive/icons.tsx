// Line icons for the honey-garden console, following the project's existing
// inline-SVG convention (see web/src/ConsoleIcon.tsx). No icon library is added.
//
// Bees come in two flavours, both inline SVG:
//   BeeIcon     - colour, token-driven, for the brand, empty states and Hive cards
//   BeeLineIcon - single currentColor, for nav and buttons at 16-20px
import { useId } from "react";

export type IconName =
  | "hive"
  | "hexagon"
  | "crown"
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
  | "play";

const paths: Record<IconName, string | string[]> = {
  hive: "M12 2.6 20.1 7.3v9.4L12 21.4 3.9 16.7V7.3L12 2.6Z",
  hexagon: "M12 2.6 20.1 7.3v9.4L12 21.4 3.9 16.7V7.3L12 2.6Z",
  crown: "M4.5 18h15M5.4 17 3.6 8.4l4.9 3.1L12 6l3.5 5.5 4.9-3.1L18.6 17",
  // A honeycomb cell with a bee in it.
  swarm: [
    "M12 2.2 20.4 7.1v9.8L12 21.8 3.6 16.9V7.1L12 2.2Z",
    "M12 11.8c1.9 0 3.4 1.5 3.4 3.4s-1.5 3.4-3.4 3.4-3.4-1.5-3.4-3.4 1.5-3.4 3.4-3.4Z",
    "M8.9 14.2h6.2M9 16.7h6",
    "M9.5 12.1c-1.7-1.8-3.8-1.5-3.4.7M14.5 12.1c1.7-1.8 3.8-1.5 3.4.7",
    "M10 14.6c-1.4-.9-3-.5-2.7.9M14 14.6c1.4-.9 3-.5 2.7.9",
    "M12 8.4a1.4 1.4 0 1 0 0 2.8 1.4 1.4 0 0 0 0-2.8",
  ],
  chat: "M4 5h16v11H9.5L4 20V5Z",
  timeline: ["M7 4v16", "M7 5h7a3 3 0 0 1 0 6H7", "M7 15h5a3 3 0 0 1 0 6H7"],
  inbox: "M3 13h5l1.8 3h4.4L16 13h5M5.5 5h13l2.5 8v6H3v-6l2.5-8Z",
  memory: ["M12 3 4 7.2v9.6L12 21l8-4.2V7.2L12 3Z", "M4 7.2 12 11.4l8-4.2M12 11.4V21"],
  settings:
    "M10 2h4l.5 2.3c.6.2 1.2.4 1.7.7l2-.9 2.8 2.8-.9 2c.3.5.5 1.1.7 1.7L23 10v4l-2.2.5c-.2.6-.4 1.2-.7 1.7l.9 2-2.8 2.8-2-.9c-.5.3-1.1.5-1.7.7L14 23h-4l-.5-2.2c-.6-.2-1.2-.4-1.7-.7l-2 .9-2.8-2.8.9-2c-.3-.5-.5-1.1-.7-1.7L1 14v-4l2.2-.5c.2-.6.4-1.2.7-1.7l-.9-2L5.8 3l2 .9c.5-.3 1.1-.5 1.7-.7L10 2ZM12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z",
  shield: ["M12 3 4 6v6.2c0 4.8 3.4 7.7 8 8.8 4.6-1.1 8-4 8-8.8V6l-8-3Z", "m8.6 12 2.2 2.2 4.6-4.6"],
  branch: "M6 3v12a4 4 0 0 0 4 4h8M6 8h7a4 4 0 0 0 4-4V3",
  pr: [
    "M6 7v10",
    "M6 4a3 3 0 1 0 0 6 3 3 0 0 0 0-6",
    "M18 14a3 3 0 1 0 0 6 3 3 0 0 0 0-6",
    "M18 14V9a2 2 0 0 0-2-2h-3",
    "M13 5l-2 2 2 2",
  ],
  diff: ["M6 4v16", "M13 8h6M16 5l3 3-3 3", "M13 16h6M16 13l3 3-3 3"],
  terminal: "m4 6 6 6-6 6M12.5 18h7.5",
  clock: "M12 7v5l3 2M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20Z",
  check: "m5 12 4 4L19 6",
  alert: "M12 8v5m0 3v.1M12 3 2 21h20L12 3Z",
  close: "m6 6 12 12M6 18 18 6",
  plus: "M12 5v14M5 12h14",
  search: "m16 16 5 5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0",
  filter: "M3 5h18l-7 8.2V20l-4-2.2v-4.6L3 5Z",
  send: "M4 12 20 4l-8 16-2-6-6-2Z",
  back: "M19 12H5m5-5-5 5 5 5",
  chevron: "m9 5 7 7-7 7",
  external: "M14 4h6v6M20 4l-8 8M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5",
  user: "M12 12a4 4 0 1 0 0-8 4 4 0 0 0 0 8M4 21v-1a6 6 0 0 1 16 0v1",
  refresh: "M20 11a8 8 0 1 0-2.3 6.3M20 5v6h-6",
  pause: "M9 5v14M15 5v14",
  play: "M7 4.5 19 12 7 19.5v-15Z",
};

export function Icon({
  name,
  size = 18,
  className,
}: {
  name: IconName;
  size?: number;
  className?: string;
}) {
  const d = paths[name];
  const list = Array.isArray(d) ? d : [d];
  return (
    <svg
      className={className ? `hg-icon ${className}` : "hg-icon"}
      width={size}
      height={size}
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
      focusable="false"
    >
      {list.map((path, index) => (
        <path key={index} d={path} />
      ))}
    </svg>
  );
}

/** Regular hexagon points for a pointy-top cell, used by avatars and decor. */
export function hexPoints(cx: number, cy: number, r: number): string {
  return Array.from({ length: 6 }, (_, index) => {
    const angle = (Math.PI / 180) * (60 * index - 90);
    return `${(cx + r * Math.cos(angle)).toFixed(2)},${(cy + r * Math.sin(angle)).toFixed(2)}`;
  }).join(" ");
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

// ---------------------------------------------------------------------------
// Bee geometry. Drawn upright in a 48x48 box and tilted ~35 degrees, so both
// variants share the same silhouette.
// ---------------------------------------------------------------------------
const BEE_TILT = "rotate(-35 24 24)";
const BEE_ABDOMEN = { cx: 24, cy: 32.5, rx: 8.4, ry: 9.5 };
const BEE_HEAD = { cx: 24, cy: 13.8, r: 4.9 };
const BEE_THORAX = { cx: 24, cy: 21, rx: 4.7, ry: 3.5 };
// Bees have two wing pairs: the larger forewings above, the smaller hindwings
// tucked underneath and further out.
const BEE_FOREWING_LEFT = { cx: 15, cy: 13, rx: 8.2, ry: 3.4, rotate: 29 };
const BEE_FOREWING_RIGHT = { cx: 33, cy: 13, rx: 8.2, ry: 3.4, rotate: -29 };
const BEE_HINDWING_LEFT = { cx: 15.8, cy: 18.7, rx: 6.5, ry: 2.6, rotate: 22 };
const BEE_HINDWING_RIGHT = { cx: 32.2, cy: 18.7, rx: 6.5, ry: 2.6, rotate: -22 };
const BEE_ANTENNA_TIPS: [number, number][] = [
  [17.4, 3.9],
  [30.6, 3.9],
];
const BEE_ANTENNA_LEFT = "M21.9 9.9C20.1 6.9 18.7 5.3 17.4 3.9";
const BEE_ANTENNA_RIGHT = "M26.1 9.9C27.9 6.9 29.3 5.3 30.6 3.9";
// Wide where it meets the abdomen, tapering to a sharp point at the tip.
const BEE_STINGER = "21.9,40.5 26.1,40.5 24,46";
// Evenly spaced bands across the abdomen (3 colour, 2 line for small sizes).
const COLOR_STRIPES = [25.1, 30.7, 36.4];
const LINE_STRIPES = [26.9, 34.5];

type Wing = { cx: number; cy: number; rx: number; ry: number; rotate: number };

function WingShape({ wing, className }: { wing: Wing; className: string }) {
  return (
    <ellipse
      className={className}
      cx={wing.cx}
      cy={wing.cy}
      rx={wing.rx}
      ry={wing.ry}
      transform={`rotate(${wing.rotate} ${wing.cx} ${wing.cy})`}
    />
  );
}

function BeeWings({ fore, hind }: { fore: string; hind: string }) {
  return (
    <>
      <WingShape wing={BEE_HINDWING_LEFT} className={hind} />
      <WingShape wing={BEE_HINDWING_RIGHT} className={hind} />
      <WingShape wing={BEE_FOREWING_LEFT} className={fore} />
      <WingShape wing={BEE_FOREWING_RIGHT} className={fore} />
    </>
  );
}

/**
 * Colour bee. Every part is painted from a --hg-bee-* token, and the dark head,
 * thorax, antennae and stinger sit on a light keyline halo, so the bee stays
 * readable on honey, on white and on a dark surface.
 */
export function BeeIcon({
  size = 24,
  className,
  title,
}: {
  size?: number;
  className?: string;
  title?: string;
}) {
  const clipId = `hg-bee-body-${useId().replace(/[:]/g, "")}`;
  return (
    <svg
      className={className ? `hg-icon hg-bee ${className}` : "hg-icon hg-bee"}
      width={size}
      height={size}
      viewBox="0 0 48 48"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      <defs>
        <clipPath id={clipId}>
          <ellipse
            cx={BEE_ABDOMEN.cx}
            cy={BEE_ABDOMEN.cy}
            rx={BEE_ABDOMEN.rx}
            ry={BEE_ABDOMEN.ry}
          />
        </clipPath>
      </defs>
      <g transform={BEE_TILT}>
        {/* Halo first: it has to sit behind the translucent wings. */}
        <g className="hg-bee-halo">
          <ellipse cx={BEE_THORAX.cx} cy={BEE_THORAX.cy} rx={BEE_THORAX.rx} ry={BEE_THORAX.ry} />
          <circle cx={BEE_HEAD.cx} cy={BEE_HEAD.cy} r={BEE_HEAD.r} />
          {BEE_ANTENNA_TIPS.map(([cx, cy]) => (
            <circle key={`halo-${cx}`} cx={cx} cy={cy} r={1.4} />
          ))}
          <path d={BEE_ANTENNA_LEFT} />
          <path d={BEE_ANTENNA_RIGHT} />
          <polygon points={BEE_STINGER} />
        </g>
        <BeeWings fore="hg-bee-wing" hind="hg-bee-wing hg-bee-hindwing" />
        <polygon className="hg-bee-stinger" points={BEE_STINGER} />
        <ellipse
          className="hg-bee-abdomen"
          cx={BEE_ABDOMEN.cx}
          cy={BEE_ABDOMEN.cy}
          rx={BEE_ABDOMEN.rx}
          ry={BEE_ABDOMEN.ry}
        />
        <g clipPath={`url(#${clipId})`}>
          {COLOR_STRIPES.map(y => (
            <rect key={y} className="hg-bee-stripe" x="14" y={y} width="20" height="3.6" rx="1.8" />
          ))}
        </g>
        <ellipse
          className="hg-bee-abdomen-line"
          cx={BEE_ABDOMEN.cx}
          cy={BEE_ABDOMEN.cy}
          rx={BEE_ABDOMEN.rx}
          ry={BEE_ABDOMEN.ry}
        />
        <ellipse
          className="hg-bee-thorax"
          cx={BEE_THORAX.cx}
          cy={BEE_THORAX.cy}
          rx={BEE_THORAX.rx}
          ry={BEE_THORAX.ry}
        />
        <circle className="hg-bee-head" cx={BEE_HEAD.cx} cy={BEE_HEAD.cy} r={BEE_HEAD.r} />
        <path className="hg-bee-antenna" d={BEE_ANTENNA_LEFT} />
        <path className="hg-bee-antenna" d={BEE_ANTENNA_RIGHT} />
        {BEE_ANTENNA_TIPS.map(([cx, cy]) => (
          <circle key={`tip-${cx}`} className="hg-bee-tip" cx={cx} cy={cy} r={1.4} />
        ))}
      </g>
    </svg>
  );
}

/**
 * Single-colour bee for nav and buttons. Stripes stay solid and the wings keep
 * their outline so the shape still reads at 16px.
 */
export function BeeLineIcon({
  size = 24,
  className,
  title,
}: {
  size?: number;
  className?: string;
  title?: string;
}) {
  const clipId = `hg-bee-line-body-${useId().replace(/[:]/g, "")}`;
  return (
    <svg
      className={className ? `hg-icon hg-bee-line ${className}` : "hg-icon hg-bee-line"}
      width={size}
      height={size}
      viewBox="0 0 48 48"
      role={title ? "img" : undefined}
      aria-label={title}
      aria-hidden={title ? undefined : true}
      focusable="false"
    >
      <defs>
        <clipPath id={clipId}>
          <ellipse
            cx={BEE_ABDOMEN.cx}
            cy={BEE_ABDOMEN.cy}
            rx={BEE_ABDOMEN.rx}
            ry={BEE_ABDOMEN.ry}
          />
        </clipPath>
      </defs>
      <g transform={BEE_TILT}>
        <BeeWings fore="hg-bee-line-wing" hind="hg-bee-line-wing hg-bee-line-hindwing" />
        <g clipPath={`url(#${clipId})`}>
          {LINE_STRIPES.map(y => (
            <rect key={y} className="hg-bee-line-stripe" x="14" y={y} width="20" height="3.6" rx="1.8" />
          ))}
        </g>
        <ellipse
          className="hg-bee-line-abdomen"
          cx={BEE_ABDOMEN.cx}
          cy={BEE_ABDOMEN.cy}
          rx={BEE_ABDOMEN.rx}
          ry={BEE_ABDOMEN.ry}
        />
        <ellipse
          className="hg-bee-line-thorax"
          cx={BEE_THORAX.cx}
          cy={BEE_THORAX.cy}
          rx={BEE_THORAX.rx}
          ry={BEE_THORAX.ry}
        />
        <circle className="hg-bee-line-head" cx={BEE_HEAD.cx} cy={BEE_HEAD.cy} r={BEE_HEAD.r} />
        <path className="hg-bee-line-antenna" d={BEE_ANTENNA_LEFT} />
        <path className="hg-bee-line-antenna" d={BEE_ANTENNA_RIGHT} />
      </g>
    </svg>
  );
}
