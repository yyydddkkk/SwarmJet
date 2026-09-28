import { useRef, useState, type ClipboardEvent } from "react";
import ReactMarkdown, { defaultUrlTransform } from "react-markdown";
import remarkGfm from "remark-gfm";
import { uploadMarkdown } from "./workbench-api";
import { ErrorBox } from "./ui";

function resolveUrl(url: string, baseUrl?: string) {
  const safe = defaultUrlTransform(url);
  if (!safe || !baseUrl || /^[a-z][a-z0-9+.-]*:/i.test(safe)) return safe;
  try { return new URL(safe, baseUrl).href; } catch { return ""; }
}

function imageUrl(src: string | undefined, baseUrl?: string, uploadPath?: string) {
  if (!src || !baseUrl || !uploadPath) return src;
  try {
    const source = new URL(src);
    if (source.origin !== new URL(baseUrl).origin) return src;
    const match = /^\/(?:-\/project\/\d+\/)?uploads\/([a-f\d]{32})\/([^/]+)$/i.exec(source.pathname);
    if (!match) return src;
    return `/api${uploadPath}/uploads/${match[1]}/${encodeURIComponent(decodeURIComponent(match[2]))}`;
  } catch { return src; }
}

function MarkdownImage({ src, alt, baseUrl, uploadPath }: { src?: string; alt?: string; baseUrl?: string; uploadPath?: string }) {
  const [failed, setFailed] = useState(false);
  if (!src) return null;
  if (failed) return <a href={src} target="_blank" rel="noreferrer">图片{alt ? `“${alt}”` : ""}无法显示，在 GitLab 查看</a>;
  return <img src={imageUrl(src, baseUrl, uploadPath)} alt={alt || ""} onError={() => setFailed(true)} />;
}

export function MarkdownContent({ value, baseUrl, uploadPath }: { value: string; baseUrl?: string; uploadPath?: string }) {
  return <div className="markdown-content"><ReactMarkdown remarkPlugins={[remarkGfm]} urlTransform={url => resolveUrl(url, baseUrl)} components={{ img: ({ src, alt }) => <MarkdownImage src={src} alt={alt} baseUrl={baseUrl} uploadPath={uploadPath} /> }}>{value}</ReactMarkdown></div>;
}

export default function MarkdownEditor({ value, onChange, uploadPath, imageProxyPath = uploadPath, baseUrl, label = "描述", rows = 10, initialPreview = false }: {
  value: string; onChange: (value: string) => void; uploadPath?: string; imageProxyPath?: string; baseUrl?: string; label?: string; rows?: number; initialPreview?: boolean;
}) {
  const [preview, setPreview] = useState(initialPreview);
  const [uploading, setUploading] = useState(false);
  const [error, setError] = useState("");
  const input = useRef<HTMLTextAreaElement>(null);
  const files = useRef<HTMLInputElement>(null);
  const upload = async (file: File) => {
    if (!uploadPath) return;
    setUploading(true); setError("");
    try {
      const result = await uploadMarkdown(uploadPath, file);
      const markdown = result.markdown || (result.url ? `![${file.name}](${result.url})` : "");
      const position = input.current?.selectionStart ?? value.length;
      onChange(`${value.slice(0, position)}${value && position === value.length ? "\n" : ""}${markdown}${value.slice(position)}`);
    } catch (cause) { setError((cause as Error).message); }
    finally { setUploading(false); }
  };
  const onPaste = (event: ClipboardEvent<HTMLTextAreaElement>) => {
    const image = Array.from(event.clipboardData.files).find(file => file.type.startsWith("image/"));
    if (image && uploadPath) { event.preventDefault(); void upload(image); }
  };
  return <div className="markdown-editor">
    <div className="markdown-editor-head"><strong>{label}</strong><div><button type="button" aria-pressed={!preview} onClick={() => setPreview(false)}>编辑</button><button type="button" aria-pressed={preview} onClick={() => setPreview(true)}>预览</button></div></div>
    {preview ? <div className="markdown-preview" aria-label={`${label}预览`}>{value ? <MarkdownContent value={value} baseUrl={baseUrl} uploadPath={imageProxyPath} /> : <p className="muted">暂无内容</p>}</div> : <textarea ref={input} aria-label={label} rows={rows} value={value} onChange={event => onChange(event.target.value)} onPaste={onPaste} />}
    <div className="markdown-editor-foot"><span>支持 Markdown；图片可粘贴到编辑框。</span>{uploadPath && <><input ref={files} type="file" hidden onChange={event => { const file = event.target.files?.[0]; if (file) void upload(file); event.target.value = ""; }} /><button type="button" disabled={uploading} onClick={() => files.current?.click()}>{uploading ? "上传中…" : "上传附件"}</button></>}</div>
    <ErrorBox error={error} />
  </div>;
}
