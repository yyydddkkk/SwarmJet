// #/icons — the pixel reference page: every icon at 16/32/48, both bee sizes and
// the Queen's crown, twelve hashed Forager avatars and the loader, each shown on
// a light and on a dark surface.
import type { ReactNode } from "react";
import { BeeLoader, Panel } from "./components";
import { Icon, ICON_STEPS, type IconName } from "./icons";
import { ForagerAvatar, PixelIcon } from "./pixel";

const ICON_NAMES: IconName[] = [
  "hive",
  "hexagon",
  "crown",
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
];

const BEE_SIZES = [16, 32, 48];
const BEE_32_SIZES = [32, 64, 96];

const FORAGER_IDS = [
  "forager-7f3a",
  "forager-2b91",
  "forager-5c40",
  "forager-9d12",
  "forager-3e77",
  "forager-1a2b",
  "forager-4c5d",
  "forager-e08f",
  "forager-b610",
  "forager-88c3",
  "forager-d2a7",
  "forager-6b45",
];

function Sizes({ children }: { children: ReactNode }) {
  return <span className="hg-px-sizes">{children}</span>;
}

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
          ? "深色底 #23231F · 描边 token 切成奶白，蜜蜂与皇冠保持原色"
          : "奶白底 #FBF8F1 / 白面板 · 描边 token 为深棕近黑"}
      </p>
      {children}
    </section>
  );
}

function IconSet() {
  return (
    <Row label="图标集" note="16×16 网格，currentColor，16 / 32 / 48px">
      {ICON_NAMES.map(name => (
        <Cell key={name} label={name}>
          <Sizes>
            {ICON_STEPS.map(size => (
              <Icon key={size} name={name} size={size} />
            ))}
          </Sizes>
        </Cell>
      ))}
    </Row>
  );
}

function BeeSet() {
  return (
    <>
      <Row label="bee-16" note="16×16 网格，16 / 32 / 48px">
        <Cell label="bee-16">
          <Sizes>
            {BEE_SIZES.map(size => (
              <PixelIcon key={size} name="bee-16" size={size} />
            ))}
          </Sizes>
        </Cell>
        <Cell label="queen-16">
          <Sizes>
            {BEE_SIZES.map(size => (
              <PixelIcon key={size} name="queen-16" size={size} />
            ))}
          </Sizes>
        </Cell>
      </Row>
      <Row label="bee-32" note="32×32 网格，32 / 64 / 96px">
        <Cell label="bee-32">
          <Sizes>
            {BEE_32_SIZES.map(size => (
              <PixelIcon key={size} name="bee-32" size={size} />
            ))}
          </Sizes>
        </Cell>
        <Cell label="queen-32">
          <Sizes>
            {BEE_32_SIZES.map(size => (
              <PixelIcon key={size} name="queen-32" size={size} />
            ))}
          </Sizes>
        </Cell>
      </Row>
      <Row label="空状态" note="32×32 网格，蜂巢加一只蜂，96px">
        <Cell label="comb-32">
          <Sizes>
            <PixelIcon name="comb-32" size={96} />
          </Sizes>
        </Cell>
      </Row>
    </>
  );
}

function LoaderSet() {
  return (
    <Row label="加载动画" note="两帧翅膀 + 2px 浮动；reduced-motion 停在第一帧">
      <Cell label="bee-loader">
        <BeeLoader label="正在飞行…" />
      </Cell>
    </Row>
  );
}

function AvatarSet() {
  return (
    <Row label="Forager 头像" note="FNV-1a 哈希 id：身体色 / 条纹 2-3 / 配饰 / 朝向">
      {FORAGER_IDS.map(id => (
        <Cell key={id} label={id.replace("forager-", "")}>
          <Sizes>
            <ForagerAvatar id={id} size={32} />
            <ForagerAvatar id={id} size={16} />
          </Sizes>
        </Cell>
      ))}
    </Row>
  );
}

export default function IconLab() {
  return (
    <div className="hg-page">
      <header className="hg-page-head">
        <div>
          <h1>像素规范</h1>
          <p className="hg-page-sub">
            像素只点缀图标、logo、头像、空状态和加载动画：内联 SVG，一像素一个 rect，相邻同色合并成
            path，只在整数倍尺寸下显示。正文、代码、diff 和表格保持原有字体。
          </p>
        </div>
      </header>

      <section className="hg-px-lab" id="pixel-icons" aria-label="像素图标与蜜蜂">
        <div className="hg-icon-lab">
          <Surface>
            <IconSet />
            <BeeSet />
            <LoaderSet />
          </Surface>
          <Surface dark>
            <IconSet />
            <BeeSet />
            <LoaderSet />
          </Surface>
        </div>
      </section>

      <section className="hg-px-lab" id="forager-avatars" aria-label="Forager 头像">
        <div className="hg-icon-lab">
          <Surface>
            <AvatarSet />
          </Surface>
          <Surface dark>
            <AvatarSet />
          </Surface>
        </div>
      </section>

      <Panel title="调色板" subtitle="所有像素图只从这里取色，浅色与深色各一套 token">
        <ul className="hg-px-palette">
          {[
            ["--hg-px-body", "身体主色（蜂蜜黄，头像按 id 换成 6 种暖色之一）"],
            ["--hg-px-shade", "深黄，身体阴影与条纹旁的高光"],
            ["--hg-px-ink", "深棕近黑：描边、条纹、眼睛、触角"],
            ["--hg-px-wing", "浅蓝白：翅膀"],
            ["--hg-px-blush", "粉：腮红"],
            ["--hg-px-gold", "金：Queen 的皇冠"],
            ["--hg-px-shine", "奶白：眼睛高光"],
          ].map(([token, use]) => (
            <li key={token}>
              <span className="hg-px-swatch" style={{ background: `var(${token})` }} aria-hidden="true" />
              <code>{token}</code>
              <span className="hg-muted">{use}</span>
            </li>
          ))}
        </ul>
      </Panel>
    </div>
  );
}
