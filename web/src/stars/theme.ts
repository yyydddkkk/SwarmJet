import { useSyncExternalStore } from "react";
export const THEMES = {
  bright: {
    label: "明亮",
    dark: false,
    colors: {
      paper: "#F4F3EF",
      ink: "#131927",
      ink2: "rgba(19,25,39,.74)",
      ink3: "rgba(19,25,39,.46)",
      hair: "rgba(19,25,39,.10)",
      accent: "#8A6716",
      veil: "rgba(244,243,239,.96)",
    },
    render: {
      grain: 0,
      dotted: false,
      tick: 44,
      halo: 0.62,
      haloColor: "#FFFFFF",
      lw: 1.2,
      sz: 1.15,
      starA: 0.32,
      grid: false,
    },
  },
  paper: {
    label: "纸本",
    dark: false,
    colors: {
      paper: "#E6E5DF",
      ink: "#1B2233",
      ink2: "rgba(27,34,51,.70)",
      ink3: "rgba(27,34,51,.48)",
      hair: "rgba(27,34,51,.12)",
      accent: "#94701F",
      veil: "rgba(230,229,223,.95)",
    },
    render: {
      grain: 0.045,
      dotted: true,
      tick: 22,
      halo: 0,
      haloColor: "#FFFFFF",
      lw: 1,
      sz: 1,
      starA: 0.5,
      grid: true,
    },
  },
  night: {
    label: "夜空",
    dark: true,
    colors: {
      paper: "#0A1020",
      ink: "#DCE3EF",
      ink2: "rgba(220,227,239,.70)",
      ink3: "rgba(220,227,239,.44)",
      hair: "rgba(220,227,239,.09)",
      accent: "#D7B35E",
      veil: "rgba(10,16,32,.95)",
    },
    render: {
      grain: 0.025,
      dotted: true,
      tick: 22,
      halo: 0,
      haloColor: "#FFFFFF",
      lw: 1,
      sz: 1,
      starA: 0.75,
      grid: true,
    },
  },
};
export type ThemeId = keyof typeof THEMES;
export type ThemeChoice = ThemeId | "system";
export type VisualStyle = "classic" | "simple";
const read = (key: string) => {
  try {
    return localStorage.getItem(key);
  } catch {
    return null;
  }
};
const write = (key: string, value: string) => {
  try {
    localStorage.setItem(key, value);
  } catch {
    /* Preferences are optional. */
  }
};
const media = matchMedia("(prefers-color-scheme: dark)");
const saved = read("sj-theme");
let choice: ThemeChoice =
  saved && (saved === "system" || Object.hasOwn(THEMES, saved))
    ? (saved as ThemeChoice)
    : "system";
let style: VisualStyle = read("sj-style") === "simple" ? "simple" : "classic";
let grid: boolean | undefined =
  read("sj-grid") === null ? undefined : read("sj-grid") === "true";
const listeners = new Set<() => void>();
function resolve() {
  const id =
    choice === "system" ? (media.matches ? "night" : "bright") : choice;
  const theme = THEMES[id];
  return {
    choice,
    id,
    style,
    ...theme,
    render: {
      ...theme.render,
      lw: style === "simple" ? 1.4 : theme.render.lw,
      sz: style === "simple" ? 1 : theme.render.sz,
      grid: grid ?? theme.render.grid,
    },
  };
}
let state = resolve();
function apply() {
  state = resolve();
  const root = document.documentElement;
  root.dataset.theme = state.id;
  root.dataset.style = style;
  for (const [key, value] of Object.entries(state.colors))
    root.style.setProperty(`--${key}`, value);
  root.style.setProperty(
    "--title",
    style === "simple"
      ? '"IBM Plex Sans","Noto Sans SC",sans-serif'
      : '"IM Fell English","Noto Serif SC",serif',
  );
  root.style.setProperty(
    "--body",
    style === "simple"
      ? '"IBM Plex Sans","Noto Sans SC",sans-serif'
      : '"Source Serif 4","Noto Serif SC",serif',
  );
  listeners.forEach((fn) => fn());
}
export const themeStore = {
  getSnapshot: () => state,
  subscribe(fn: () => void) {
    listeners.add(fn);
    return () => {
      listeners.delete(fn);
    };
  },
  setTheme(value: ThemeChoice) {
    choice = value;
    write("sj-theme", value);
    apply();
  },
  setStyle(value: VisualStyle) {
    style = value;
    write("sj-style", value);
    apply();
  },
  setGrid(value: boolean) {
    grid = value;
    write("sj-grid", String(value));
    apply();
  },
};
media.addEventListener("change", apply);
apply();
export const useTheme = () =>
  useSyncExternalStore(
    themeStore.subscribe,
    themeStore.getSnapshot,
    themeStore.getSnapshot,
  );
