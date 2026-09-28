export function Icon({
  name,
}: {
  name:
    | "overview"
    | "tasks"
    | "issue"
    | "activity"
    | "projects"
    | "nodes"
    | "runtime"
    | "images"
    | "logout"
    | "plus"
    | "search"
    | "clock"
    | "arrow"
    | "bolt"
    | "branch"
    | "settings"
    | "members"
    | "identity"
    | "alert"
    | "check";
}) {
  const paths = {
    overview: "M3 3h7v7H3zM14 3h7v7h-7zM3 14h7v7H3zM14 14h7v7h-7z",
    issue: "M4 4h16v5a3 3 0 0 0 0 6v5H4v-5a3 3 0 0 0 0-6V4Zm8 1v3m0 3v2m0 3v3",
    activity: "M2 12h4l3-7 5 14 3-7h5",
    bolt: "m13 2-9 12h7l-1 8 10-13h-7l1-7Z",
    branch: "M6 3v12a4 4 0 0 0 4 4h8M6 8h7a4 4 0 0 0 4-4V3",
    settings: "M10 2h4l.5 2.3c.6.2 1.2.4 1.7.7l2-.9 2.8 2.8-.9 2c.3.5.5 1.1.7 1.7L23 10v4l-2.2.5c-.2.6-.4 1.2-.7 1.7l.9 2-2.8 2.8-2-.9c-.5.3-1.1.5-1.7.7L14 23h-4l-.5-2.2c-.6-.2-1.2-.4-1.7-.7l-2 .9-2.8-2.8.9-2c-.3-.5-.5-1.1-.7-1.7L1 14v-4l2.2-.5c.2-.6.4-1.2.7-1.7l-.9-2L5.8 3l2 .9c.5-.3 1.1-.5 1.7-.7L10 2ZM12 9a3 3 0 1 0 0 6 3 3 0 0 0 0-6Z",
    members: "M9 11a4 4 0 1 0 0-8 4 4 0 0 0 0 8ZM2 21v-2a7 7 0 0 1 14 0v2H2Zm15-13a3 3 0 1 1 0 6m-1 3a5 5 0 0 1 6 4",
    identity: "M12 3a7 7 0 0 0-7 7v4a7 7 0 0 0 14 0v-4a7 7 0 0 0-7-7Zm-3 8h.01M15 11h.01M9 16c2 2 4 2 6 0",
    alert: "M12 8v5m0 3v.1M12 3 2 21h20L12 3Z",
    check: "m5 12 4 4L19 6",
    tasks: "M8 5h12M8 12h12M8 19h12M3 5h.01M3 12h.01M3 19h.01",
    projects:
      "M3 7V5a2 2 0 0 1 2-2h5l2 3h7a2 2 0 0 1 2 2v11a2 2 0 0 1-2 2H5a2 2 0 0 1-2-2V7Z",
    nodes:
      "M4 3h16a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2V5a2 2 0 0 1 2-2Zm0 10h16a2 2 0 0 1 2 2v4a2 2 0 0 1-2 2H4a2 2 0 0 1-2-2v-4a2 2 0 0 1 2-2ZM6 7h.01M6 17h.01",
    runtime: "m5 7 5 5-5 5M13 17h6",
    images: "m12 3 9 5-9 5-9-5 9-5Zm-9 9 9 5 9-5M3 16l9 5 9-5",
    logout: "M9 4H5v16h4M9 12h12m-5-5 5 5-5 5",
    plus: "M12 5v14M5 12h14",
    search: "m16 16 5 5M18 10a8 8 0 1 1-16 0 8 8 0 0 1 16 0",
    clock: "M12 7v5l3 2M12 22a10 10 0 1 0 0-20 10 10 0 0 0 0 20Z",
    arrow: "M5 12h14m-6-6 6 6-6 6",
  };
  return (
    <svg
      className="ui-icon"
      width="18"
      height="18"
      viewBox="0 0 24 24"
      fill="none"
      stroke="currentColor"
      strokeWidth="1.6"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d={paths[name]} />
    </svg>
  );
}
