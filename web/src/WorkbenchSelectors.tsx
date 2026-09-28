import { useEffect, useId, useRef, useState, type KeyboardEvent, type ReactNode } from "react";
import { api } from "./api";
import type { Assignee } from "./workbench-api";

export type WorkbenchLabel = { name: string; color?: string };
type Options = { labels: WorkbenchLabel[]; assignees: Assignee[] };

// Reuse the same short-lived request when both selectors open together.
const optionRequests = new Map<string, { until: number; result: Promise<Options> }>();
function getOptions(kind: "issues" | "merge-requests", projectId: string, repositoryId: string, search: string) {
  const key = `${kind}/${projectId}/${repositoryId}/${search}`;
  const cached = optionRequests.get(key);
  if (cached && cached.until > Date.now()) return cached.result;
  const path = `/workbench/${kind}/${encodeURIComponent(projectId)}/${encodeURIComponent(repositoryId)}/options`;
  const result = api<Options>(`${path}?search=${encodeURIComponent(search)}`).then(value => ({ labels: value.labels || [], assignees: value.assignees || [] }));
  optionRequests.set(key, { until: Date.now() + 15_000, result });
  void result.catch(() => optionRequests.delete(key));
  return result;
}

type SelectorProps<T> = {
  title: string;
  kind: "issues" | "merge-requests";
  projectId: string;
  repositoryId: string;
  selected: T[];
  onChange: (items: T[]) => void;
  optionKey: (item: T) => string;
  optionText: (item: T) => string;
  choices: (options: Options) => T[];
  renderOption?: (item: T) => ReactNode;
  placeholder: string;
  multiple?: boolean;
  searchRemote?: boolean;
};

export function WorkbenchSelector<T>({ title, kind, projectId, repositoryId, selected, onChange, optionKey, optionText, choices, renderOption, placeholder, multiple = true, searchRemote = true }: SelectorProps<T>) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [items, setItems] = useState<T[]>([]);
  const [active, setActive] = useState(0);
  const [loading, setLoading] = useState(false);
  const [error, setError] = useState("");
  const root = useRef<HTMLDivElement>(null);
  const input = useRef<HTMLInputElement>(null);
  const listId = useId();
  const ready = Boolean(projectId && repositoryId);
  const searchKey = searchRemote ? search.trim() : "";
  useEffect(() => {
    setOpen(false); setSearch(""); setItems([]); setError("");
  }, [kind, projectId, repositoryId]);
  useEffect(() => {
    if (!open || !ready) return;
    let alive = true;
    setLoading(true); setError("");
    const timer = window.setTimeout(() => {
      void getOptions(kind, projectId, repositoryId, searchKey).then(result => {
        if (alive) { setItems(choices(result)); setActive(0); }
      }).catch(cause => { if (alive) setError((cause as Error).message); }).finally(() => { if (alive) setLoading(false); });
    }, searchKey ? 250 : 0);
    return () => { alive = false; window.clearTimeout(timer); };
  }, [open, ready, kind, projectId, repositoryId, searchKey, choices]);
  useEffect(() => {
    if (!open) return;
    const close = (event: PointerEvent) => { if (!root.current?.contains(event.target as Node)) setOpen(false); };
    document.addEventListener("pointerdown", close);
    return () => document.removeEventListener("pointerdown", close);
  }, [open]);
  const available = items.filter(item => !selected.some(current => optionKey(current) === optionKey(item)) && (searchRemote || optionText(item).toLocaleLowerCase().includes(search.trim().toLocaleLowerCase())));
  const select = (item: T) => {
    onChange(multiple ? [...selected, item] : [item]);
    input.current?.focus();
    setSearch(""); setOpen(false);
  };
  const onKeyDown = (event: KeyboardEvent<HTMLInputElement>) => {
    if (event.key === "ArrowDown") { event.preventDefault(); setOpen(true); setActive(index => Math.min(index + 1, Math.max(available.length - 1, 0))); }
    if (event.key === "ArrowUp") { event.preventDefault(); setOpen(true); setActive(index => Math.max(index - 1, 0)); }
    if (event.key === "Enter" && open && available[active]) { event.preventDefault(); select(available[active]); }
    if (event.key === "Escape") { setOpen(false); }
    if (event.key === "Backspace" && !search && selected.length) onChange(selected.slice(0, -1));
  };
  return <div className="workbench-selector" ref={root}>
    <span className="workbench-selector-label" id={`${listId}-label`}>{title}</span>
    <div className="workbench-selector-control" onClick={() => input.current?.focus()}>
      {selected.map(item => <span className="workbench-selector-chip" key={optionKey(item)}>{optionText(item)}<button type="button" aria-label={`移除${title} ${optionText(item)}`} onClick={() => onChange(selected.filter(current => optionKey(current) !== optionKey(item)))}>×</button></span>)}
      <input ref={input} type="search" role="combobox" aria-label={title} aria-autocomplete="list" aria-expanded={open && ready} aria-controls={open && ready ? listId : undefined} aria-activedescendant={open && available[active] ? `${listId}-${active}` : undefined} disabled={!ready} value={search} placeholder={ready ? placeholder : "请先选择项目和仓库"} onFocus={() => setOpen(true)} onChange={event => { setSearch(event.target.value); setOpen(true); }} onKeyDown={onKeyDown} />
    </div>
    {open && ready && <div className="workbench-selector-menu" id={listId} role="listbox" aria-label={`${title}选项`} aria-multiselectable={multiple}>
      {error ? <p role="alert">选项加载失败：{error}</p> : loading ? <p>正在加载…</p> : available.length ? available.map((item, index) => <button type="button" role="option" aria-selected={index === active} id={`${listId}-${index}`} key={optionKey(item)} className={index === active ? "active" : ""} onMouseEnter={() => setActive(index)} onClick={() => select(item)}>{renderOption ? renderOption(item) : optionText(item)}</button>) : <p>没有可选项</p>}
    </div>}
  </div>;
}

const labelChoices = (options: Options) => options.labels;
const assigneeChoices = (options: Options) => options.assignees;
const labelKey = (label: WorkbenchLabel) => label.name;
const assigneeKey = (user: Assignee) => String(user.id);
const labelText = (label: WorkbenchLabel) => label.name;
const assigneeText = (user: Assignee) => user.name || user.username || `用户 ${user.id}`;

export function LabelSelector(props: Omit<SelectorProps<WorkbenchLabel>, "title" | "optionKey" | "optionText" | "choices" | "placeholder" | "searchRemote">) {
  return <WorkbenchSelector {...props} title="标签" optionKey={labelKey} optionText={labelText} choices={labelChoices} placeholder="搜索标签…" searchRemote={false} renderOption={label => <><span className="workbench-label-dot" style={{ backgroundColor: label.color || "#8b9bac" }} />{label.name}</>} />;
}
export function AssigneeSelector(props: Omit<SelectorProps<Assignee>, "title" | "optionKey" | "optionText" | "choices" | "placeholder">) {
  return <WorkbenchSelector {...props} title="负责人" optionKey={assigneeKey} optionText={assigneeText} choices={assigneeChoices} placeholder="搜索姓名或用户名…" renderOption={user => <><strong>{assigneeText(user)}</strong>{user.username && <small>@{user.username}</small>}</>} />;
}
