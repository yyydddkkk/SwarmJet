import { useCallback, useEffect, useState } from "react";
import { api } from "./api";
import type { Task } from "./types";
import { ErrorBox } from "./ui";
import { useToast } from "./ToastProvider";
import "./workbench-pages.css";

type Record = { digest: string; expectedDigest: string; checks: { index: number; confirmed: boolean }[]; note: string };

export default function ManualMergePanel({ task, refresh }: { task: Task; refresh: () => Promise<void> }) {
  const toast = useToast();
  const path = `/tasks/${encodeURIComponent(task.id)}`;
  const [record, setRecord] = useState<Record | null>(null);
  const [checked, setChecked] = useState<boolean[]>([]);
  const [note, setNote] = useState("");
  const [busy, setBusy] = useState(false); const [error, setError] = useState("");
  const items = [
    ...(task.humanAcceptance?.prerequisites || []).map((text, index) => ({ label: `前置条件 ${index + 1}`, text })),
    ...(task.humanAcceptance?.steps || []).map((step, index) => ({ label: `验收步骤 ${index + 1}`, text: `${step.action} → 预期：${step.expected}` })),
  ];
  const load = useCallback(async () => {
    try { const result = await api<Record>(`${path}/manual-acceptance`); setRecord(result); setChecked(items.map((_, index) => result.digest === result.expectedDigest && !!result.checks?.find(check => check.index === index)?.confirmed)); setNote(result.digest === result.expectedDigest ? result.note || "" : ""); setError(""); }
    catch (cause) { setError((cause as Error).message); }
  // Checklist labels may change with the task version. Its evidence digest is the source of truth.
  }, [path, task.reviewDigest, task.updatedAt]);
  useEffect(() => { void load(); }, [load]);
  const save = async () => { if (!record) return; setBusy(true); setError(""); try { const result = await api<Record>(`${path}/manual-acceptance`, "PUT", { digest: record.expectedDigest, checks: items.map((_, index) => ({ index, confirmed: checked[index] || false })), note }); setRecord({ ...result, expectedDigest: record.expectedDigest }); await refresh(); toast.success("人工验收已保存，并绑定当前审查与提交版本。"); } catch (cause) { setError((cause as Error).message); } finally { setBusy(false); } };
  const merge = async () => { setBusy(true); setError(""); try { await api(`${path}/commands`, "POST", { id: crypto.randomUUID(), kind: "merge_round" }); await refresh(); toast.success("整轮合并命令已提交，任务将逐仓库记录结果。"); } catch (cause) { setError((cause as Error).message); } finally { setBusy(false); } };
  const current = !!record && record.digest === record.expectedDigest && record.digest !== "";
  const ready = current && checked.every(Boolean) && !!note.trim();
  return <section className="panel workbench-manual-merge"><div className="panel-head"><div><h2>人工验收与整轮合并</h2><p>验收记录绑定审查摘要和每个仓库的提交 SHA。版本变化后需要重新确认。</p></div></div><div className="workbench-pad"><ErrorBox error={error} retry={() => void load()} />{!record ? <p>正在读取验收记录…</p> : <>
    {record.digest && !current && <p className="workbench-warning">审查或提交版本已变化，之前的人工验收已失效。</p>}
    {(task.humanAcceptance?.notes || []).length > 0 && <div><strong>验收说明</strong><ul>{task.humanAcceptance!.notes!.map((item, index) => <li key={index}>{item}</li>)}</ul></div>}
    {items.length ? <ol className="workbench-acceptance">{items.map((item, index) => <li key={index}><label><input type="checkbox" checked={checked[index] || false} onChange={event => setChecked(previous => { const next = [...previous]; next[index] = event.target.checked; return next; })} /><span><strong>{item.label}</strong>{item.text}</span></label></li>)}</ol> : <p className="workbench-muted">当前任务没有结构化验收项；仍需填写验收备注。</p>}
    <label className="field"><span>验收备注</span><textarea rows={3} value={note} onChange={event => setNote(event.target.value)} placeholder="记录实际验收结果与证据…" /></label>
    <div className="actions"><button disabled={busy || !checked.every(Boolean) || !note.trim()} onClick={() => void save()}>{busy ? "提交中…" : "保存人工验收"}</button><button className="primary" disabled={busy || !ready || task.stage !== "waiting_manual_merge" || task.paused} onClick={() => void merge()}>合并整轮</button></div>
    {task.stage !== "waiting_manual_merge" && <p className="workbench-muted">任务进入待人工合并阶段后可提交整轮合并。</p>}
  </>}</div></section>;
}
