// Quiet line icons for the panels; no dependency on the retired console artwork.
export function Icon({ name, size = 16 }: { name: string; size?: number }) {
  const paths: Record<string, string> = {
    check: "M3 8l3 3 7-7",
    close: "M4 4l8 8M12 4l-8 8",
    plus: "M8 3v10M3 8h10",
    refresh: "M13 6A5 5 0 1 0 13 10M13 2v4H9",
    external: "M9 2h5v5M14 2L7 9M7 3H2v11h11V9",
    alert: "M8 2L1 14h14L8 2ZM8 6v4M8 12v1",
    clock: "M8 4v4l3 2M14 8A6 6 0 1 1 2 8a6 6 0 1 1 12 0",
    shield: "M8 1L2 4v4q0 4 6 7 6-3 6-7V4Z",
    diff: "M4 1h6l3 3v11H3V1ZM6 7h4M6 10h4",
  };
  return (
    <svg
      width={size}
      height={size}
      viewBox="0 0 16 16"
      fill="none"
      stroke="currentColor"
      strokeWidth="1"
      aria-hidden="true"
    >
      <path d={paths[name] || paths.check} />
    </svg>
  );
}
export function GithubMark({ size = 16 }: { size?: number }) {
  return (
    <svg
      className="pc-icon"
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
