export function Icon({
  name,
}: {
  name:
    | "tasks"
    | "projects"
    | "nodes"
    | "runtime"
    | "images"
    | "logout"
    | "plus"
    | "search"
    | "arrow"
    | "bolt"
    | "branch"
    | "settings"
    | "alert"
    | "check";
}) {
  const paths = {
    bolt: "m13 2-9 12h7l-1 8 10-13h-7l1-7Z",
    branch: "M6 3v12a4 4 0 0 0 4 4h8M6 8h7a4 4 0 0 0 4-4V3",
    settings: "M12 3v2m0 14v2M3 12h2m14 0h2M5.6 5.6 7 7m10 10 1.4 1.4M18.4 5.6 17 7M7 17l-1.4 1.4M12 8a4 4 0 1 0 0 8 4 4 0 0 0 0-8Z",
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
