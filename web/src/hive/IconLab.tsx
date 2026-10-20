// #/legacy/icons — the pixel reference page. It mirrors the design spec
// (docs/claude/design/pixel-icons.json) exactly: the eight Forager states, the
// Queen on and off, the six hashed body colours, the 29 icons at 16 and 32px,
// the loader and the empty-state pair — each on a light and a dark surface.
import type { ReactNode } from "react";
import { BeeLoader, Panel } from "./components";
import { Icon, type IconName } from "./icons";
import {
  FORAGER_BODY_COLORS,
  FORAGER_GRIDS,
  ICON_GRIDS,
  PALETTE,
  QUEEN_GRID,
  QUEEN_OFF,
  STATE_OVERRIDES,
  type ForagerSprite,
} from "./pixel-icons";
import {
  EmptyHiveArt,
  ForagerAvatar,
  LOADER_FRAMES,
  PixelArt,
  PIXEL_SIZES,
  QueenAvatar,
  foragerBodyColor,
  fnv1a,
} from "./pixel";
import type { AgentState } from "./types";

const ICON_NAMES: IconName[] = [
  "hive",
  "swarm",
  "chat",
  "timeline",
  "inbox",
  "memory",
  "settings",
  "shield",
  "branch",
  "pr",
  "diff",
  "terminal",
  "clock",
  "check",
  "alert",
  "close",
  "plus",
  "search",
  "filter",
  "send",
  "back",
  "chevron",
  "external",
  "user",
  "refresh",
  "pause",
  "play",
  "crown",
  "bee",
];

const FORAGER_STATE_LABELS: { sprite: ForagerSprite; state: AgentState; label: string }[] = [
  { sprite: "creating", state: "creating", label: "creating" },
  { sprite: "active", state: "active", label: "active" },
  { sprite: "parked", state: "parked", label: "parked" },
  { sprite: "needs_you", state: "parked", label: "needs_you" },
  { sprite: "hibernated", state: "hibernated", label: "hibernated" },
  { sprite: "completed", state: "completed", label: "completed" },
  { sprite: "failed", state: "failed", label: "failed" },
  { sprite: "cancelled", state: "cancelled", label: "cancelled" },
];

/** Ids used to show that the hash spreads over all six body colours. */
const SAMPLE_IDS = ["forager-7f3a", "forager-2b91", "forager-5c40", "forager-9d12"];

function Cell({ label, children }: { label: string; children: ReactNode }) {
  return (
    <div className="hg-px-cell">
      <span className="hg-px-cell-art">{children}</span>
      <span className="hg-px-cell-label">{label}</span>
    </div>
  );
}

function Row({ label, note, children }: { label: string; note?: string; children: ReactNode }) {
  return (
    <div className="hg-px-row" role="group" aria-label={label}>
      <div className="hg-px-row-head">
        <span className="hg-px-row-label">{label}</span>
        {note ? <span className="hg-px-row-note">{note}</span> : null}
      </div>
      <div className="hg-px-row-body">{children}</div>
    </div>
  );
}

function Surface({ dark, children }: { dark?: boolean; children: ReactNode }) {
  return (
    <section className={dark ? "hg-icon-lab-panel hg-icon-lab-dark" : "hg-icon-lab-panel"}>
      <h2>{dark ? "深色背景" : "浅色背景"}</h2>
      <p className="hg-icon-lab-note">
        {dark
          ? "深色底 #23231F · 只换 token：a 转奶白、w/z/r 提亮，其余不变"
          : "奶白底 #FBF8F1 / 白面板 · ## 用 currentColor，其余走 --hg-px-* token"}
      </p>
      {children}
    </section>
  );
}

/** The eight states plus the Queen, straight from the spec colours. */
function BeeSet() {
  return (
    <>
      <Row label="Forager 状态" note="forager.<状态>，needs_you 用于待审批或待回答消息">
        {FORAGER_STATE_LABELS.map(({ sprite, label }) => (
          <Cell key={label} label={label}>
            <PixelArt
              rows={FORAGER_GRIDS[sprite]}
              size={32}
              colors={STATE_OVERRIDES[label === "needs_you" ? "active" : label] ?? {}}
            />
          </Cell>
        ))}
      </Row>
      <Row label="Queen" note="queen 皇冠；未开启套用 queenOff 配色">
        <Cell label="queen">
          <PixelArt rows={QUEEN_GRID} size={32} />
        </Cell>
        <Cell label="queen off">
          <PixelArt rows={QUEEN_GRID} size={32} colors={QUEEN_OFF} />
        </Cell>
        <Cell label="QueenAvatar 未开启">
          <QueenAvatar size={32} off />
        </Cell>
      </Row>
      <Row label="身体颜色" note="FNV-1a 哈希 id，只替换 y">
        {FORAGER_BODY_COLORS.map(color => (
          <Cell key={color} label={color.toLowerCase()}>
            <PixelArt rows={FORAGER_GRIDS.active} size={32} colors={{ y: color }} />
          </Cell>
        ))}
      </Row>
      <Row label="哈希示例" note="同一个 id 永远同一只蜂">
        {SAMPLE_IDS.map(id => (
          <Cell key={id} label={id.replace("forager-", "")}>
            <ForagerAvatar id={id} state="active" size={32} />
          </Cell>
        ))}
      </Row>
    </>
  );
}

function IconSet() {
  return (
    <Row label="图标集" note="16×16 网格，currentColor，16px 与 32px">
      {ICON_NAMES.map(name => (
        <Cell key={name} label={name}>
          <span className="hg-px-sizes">
            <Icon name={name} size={16} />
            <Icon name={name} size={32} />
          </span>
        </Cell>
      ))}
    </Row>
  );
}

function LoaderAndEmpty() {
  return (
    <>
      <Row label="加载动画" note="两帧扇翅 + 2px 浮动；reduced-motion 停在第一帧">
        <Cell label="bee-loader">
          <BeeLoader label="正在飞行…" />
        </Cell>
        <Cell label="frame down">
          <PixelArt rows={LOADER_FRAMES.down} size={32} />
        </Cell>
        <Cell label="frame up">
          <PixelArt rows={LOADER_FRAMES.up} size={32} />
        </Cell>
      </Row>
      <Row label="空状态" note="icons.hive 加 forager.parked">
        <Cell label="empty">
          <EmptyHiveArt />
        </Cell>
      </Row>
      <Row label="尺寸" note="只在 16 / 32 / 48 / 64px 下显示">
        {PIXEL_SIZES.map(size => (
          <Cell key={size} label={`${size}px`}>
            <PixelArt rows={FORAGER_GRIDS.active} size={size} />
          </Cell>
        ))}
      </Row>
    </>
  );
}

export default function IconLab() {
  const lightKeys = Object.keys(PALETTE.light) as (keyof typeof PALETTE.light)[];

  return (
    <div className="hg-page">
      <header className="hg-page-head">
        <div>
          <h1>像素规范</h1>
          <p className="hg-page-sub">
            全部图案来自 docs/claude/design/pixel-icons.json：16×16 字符网格，一个字符一个 1×1 rect，
            整数倍尺寸显示。正文、代码、diff 和表格保持原有字体。
          </p>
        </div>
      </header>

      <section className="hg-px-lab" id="forager-avatars" aria-label="蜜蜂：状态、Queen 与身体颜色">
        <div className="hg-icon-lab">
          <Surface>
            <BeeSet />
          </Surface>
          <Surface dark>
            <BeeSet />
          </Surface>
        </div>
      </section>

      <section className="hg-px-lab" id="pixel-icons" aria-label="像素图标、加载动画与空状态">
        <div className="hg-icon-lab">
          <Surface>
            <IconSet />
            <LoaderAndEmpty />
          </Surface>
          <Surface dark>
            <IconSet />
            <LoaderAndEmpty />
          </Surface>
        </div>
      </section>

      <Panel title="调色板" subtitle="规范文件 palette.light / palette.dark 一一对应到 --hg-px-* token">
        <ul className="hg-px-palette">
          {lightKeys.map(char => (
            <li key={char}>
              <span
                className="hg-px-swatch"
                style={{ background: `var(--hg-px-${char})` }}
                aria-hidden="true"
              />
              <code>--hg-px-{char}</code>
              <span className="hg-muted">
                {`"${char}"`} · {PALETTE.light[char]}
              </span>
            </li>
          ))}
        </ul>
      </Panel>

      <Panel title="哈希分布" subtitle="24 个 id 覆盖到的身体颜色（FNV-1a）">
        <ul className="hg-px-palette">
          {FORAGER_BODY_COLORS.map(color => {
            const hits = Array.from({ length: 24 }, (_, index) => `forager-sample-${index}`).filter(
              id => foragerBodyColor(id) === color,
            );
            return (
              <li key={color}>
                <span className="hg-px-swatch" style={{ background: color }} aria-hidden="true" />
                <code>{color}</code>
                <span className="hg-muted">{hits.length} / 24 → {hits.slice(0, 3).join(", ") || "—"}</span>
              </li>
            );
          })}
          <li>
            <span className="hg-muted">
              {`示例：forager-7f3a → ${foragerBodyColor("forager-7f3a")}（哈希 ${fnv1a("forager-7f3a")}）`}
            </span>
          </li>
        </ul>
      </Panel>
    </div>
  );
}
