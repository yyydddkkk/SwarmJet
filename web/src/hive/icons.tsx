// Line icons for the honey-garden console, following the project's existing
// inline-SVG convention (see web/src/ConsoleIcon.tsx). No icon library is added.

export type IconName =
  | "hive"
  | "hexagon"
  | "crown"
  | "bee"
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
  bee: [
    "M12 8.6c2.3 0 4 1.6 4 3.4s-1.7 3.4-4 3.4-4-1.6-4-3.4 1.7-3.4 4-3.4Z",
    "M8.4 10.6h7.2M8.2 12h7.6M8.4 13.4h7.2",
    "M10.6 8.7 9.2 6.2M13.4 8.7l1.4-2.5",
    "M9.4 9.4C6.8 6.6 4.2 7.6 5.4 10.4M14.6 9.4c2.6-2.8 5.2-1.8 4 1",
    "M12 15.4v1.8",
  ],
  swarm: [
    "M9 2.8 5 5.2v4.9L9 12.5l4-2.4V5.2L9 2.8Z",
    "M4.4 13.6l-2.6 1.5v3.2l2.6 1.5 2.6-1.5v-3.2l-2.6-1.5Z",
    "M15.4 13.6l-2.6 1.5v3.2l2.6 1.5 2.6-1.5v-3.2l-2.6-1.5Z",
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
