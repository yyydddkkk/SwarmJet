// Icon reference page (#/icons): shows both bee variants at the sizes the
// console actually uses, on a light and a dark surface.
import type { ReactNode } from "react";
import { Panel } from "./components";
import { BeeIcon, BeeLineIcon, Icon } from "./icons";

const sizes = [16, 20, 32, 64];

function Row({
  label,
  items,
}: {
  label: string;
  items: { key: string; label: string; node: ReactNode }[];
}) {
  return (
    <div className="hg-icon-row">
      <span className="hg-icon-row-label">{label}</span>
      {items.map(item => (
        <div className="hg-icon-cell" key={item.key}>
          <span className="hg-icon-cell-box">{item.node}</span>
          <span className="hg-icon-cell-label">{item.label}</span>
        </div>
      ))}
    </div>
  );
}

const beeSizes = (render: (size: number) => ReactNode) =>
  sizes.map(size => ({ key: `bee-${size}`, label: `${size}px`, node: render(size) }));

export default function IconLab() {
  return (
    <div className="hg-page">
      <header className="hg-page-head">
        <div>
          <h1>图标规范</h1>
          <p className="hg-page-sub">
            蜜蜂在浅色与深色背景下的可读性。彩色版用于品牌、空状态和 Hive 卡片，线条版用于导航和按钮。
          </p>
        </div>
      </header>

      <div className="hg-icon-lab">
        <section className="hg-icon-lab-panel" aria-label="浅色背景">
          <h2>浅色背景</h2>
          <p className="hg-icon-lab-note">奶白底 #FBF8F1 / 白面板</p>
          <Row label="彩色版 bee" items={beeSizes(size => <BeeIcon size={size} />)} />
          <Row label="线条版 bee-line" items={beeSizes(size => <BeeLineIcon size={size} />)} />
        </section>
        <section className="hg-icon-lab-panel hg-icon-lab-dark" aria-label="深色背景">
          <h2>深色背景</h2>
          <p className="hg-icon-lab-note">深色底 #23231F · 线条版跟随 currentColor</p>
          <Row label="彩色版 bee" items={beeSizes(size => <BeeIcon size={size} />)} />
          <Row label="线条版 bee-line" items={beeSizes(size => <BeeLineIcon size={size} />)} />
        </section>
      </div>

      <Panel title="其他图标" subtitle="蜂巢、蜂群与六边形，18px 与 32px">
        <Row
          label="蜂巢 / 蜂群"
          items={[
            { key: "hive-20", label: "hive 20px", node: <Icon name="hive" size={20} /> },
            { key: "hive-32", label: "hive 32px", node: <Icon name="hive" size={32} /> },
            { key: "swarm-20", label: "swarm 20px", node: <Icon name="swarm" size={20} /> },
            { key: "swarm-32", label: "swarm 32px", node: <Icon name="swarm" size={32} /> },
            { key: "hexagon-20", label: "hexagon 20px", node: <Icon name="hexagon" size={20} /> },
            { key: "crown-32", label: "crown 32px", node: <Icon name="crown" size={32} /> },
          ]}
        />
      </Panel>
    </div>
  );
}
