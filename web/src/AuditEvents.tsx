import { useEffect, useState } from "react";
import { api } from "./api";
import { ErrorBox } from "./ui";

type AuditEvent = {
  id: number;
  actorId?: string;
  actorType: string;
  action: string;
  targetType: string;
  targetId: string;
  createdAt: string;
};

export default function AuditEvents({ projectId }: { projectId?: string }) {
  const [events, setEvents] = useState<AuditEvent[]>([]);
  const [error, setError] = useState("");
  useEffect(() => {
    let active = true;
    const path = `/audit${projectId ? `?projectId=${encodeURIComponent(projectId)}` : ""}`;
    api<{ events: AuditEvent[] }>(path).then(result => {
      if (active) setEvents(result.events || []);
    }).catch(cause => {
      if (active) setError((cause as Error).message);
    });
    return () => { active = false; };
  }, [projectId]);
  return <section className="panel panel-padding" aria-label="操作审计"><h2>操作审计</h2><ErrorBox error={error} />
    <div className="access-list">{events.map(event => <div className="access-row" key={event.id}><div><strong>{event.action}</strong><small>{event.targetType} · {event.targetId} · {event.actorType === "breakglass" ? "应急管理员" : event.actorId || event.actorType}</small></div><time dateTime={event.createdAt}>{new Date(event.createdAt).toLocaleString()}</time></div>)}{!error && !events.length && <p>暂无操作记录。</p>}</div>
  </section>;
}
