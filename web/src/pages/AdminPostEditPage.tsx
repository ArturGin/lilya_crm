import { useCallback, useEffect, useRef, useState, type FormEvent } from "react";
import { Link, useNavigate, useParams } from "react-router-dom";
import {
  api,
  ApiError,
  mediaUrl,
  upload,
  type PostDetail,
  type PostStatus,
  type StepInfo,
  type TagFacet,
  type Transcript,
  type TagKind,
  type UploadHandle,
} from "../api";
import { useApp } from "../app-state";
import { useI18n, type MsgKey } from "../i18n";
import { formatBytes, formatClock, formatDate, formatMinutes } from "../util";

function needsPolling(p: PostDetail | null): boolean {
  if (!p) return false;
  const v = p.video?.status;
  const a = p.analysis?.status;
  return v === "uploaded" || v === "processing" || a === "pending" || a === "running";
}

export default function AdminPostEditPage() {
  const { id } = useParams();
  const { t } = useI18n();
  const navigate = useNavigate();
  const [post, setPost] = useState<PostDetail | null>(null);
  const [error, setError] = useState("");

  const load = useCallback(() => {
    return api
      .get<PostDetail>(`/admin/posts/${id}`)
      .then(setPost)
      .catch((e: ApiError) => setError(e.message));
  }, [id]);

  useEffect(() => {
    load();
  }, [load]);

  // Poll while the video is processing or analysis is running.
  const polling = needsPolling(post);
  useEffect(() => {
    if (!polling) return;
    const h = setInterval(load, 3000);
    return () => clearInterval(h);
  }, [polling, load]);

  if (error && !post) return <p className="form-error">{error}</p>;
  if (!post) return <p className="muted">{t("loading")}</p>;

  async function setStatus(status: PostStatus) {
    try {
      setPost(await api.patch<PostDetail>(`/admin/posts/${id}`, { status }));
    } catch (e) {
      setError((e as ApiError).message);
    }
  }

  async function remove() {
    if (!window.confirm(t("deletePostConfirm"))) return;
    await api.del(`/admin/posts/${id}`);
    navigate("/admin");
  }

  return (
    <div className="admin-edit">
      <div className="post-top">
        <Link to="/admin" className="muted">
          ← {t("posts")}
        </Link>
        {post.status === "published" && <Link to={`/p/${post.id}`}>{t("viewAsMember")} →</Link>}
      </div>

      <div className="admin-head">
        <h1>{post.title}</h1>
        <div className="row">
          <span className={`status status-${post.status}`}>{t(post.status as MsgKey)}</span>
          {post.status !== "published" && (
            <button className="btn primary" onClick={() => setStatus("published")}>
              {t("publish")}
            </button>
          )}
          {post.status === "published" && (
            <button className="btn" onClick={() => setStatus("draft")}>
              {t("unpublish")}
            </button>
          )}
          {post.status !== "archived" && (
            <button className="btn" onClick={() => setStatus("archived")}>
              {t("archive")}
            </button>
          )}
        </div>
      </div>
      {error && <p className="form-error">{error}</p>}

      <ContentForm post={post} onSaved={setPost} />
      <VideoSection post={post} onChange={setPost} />
      <AnalysisSection post={post} onChange={setPost} />
      <TagEditor post={post} onSaved={setPost} />
      <ImagesSection post={post} onChange={setPost} />

      <div className="danger-zone">
        <button className="btn danger" onClick={remove}>
          {t("deletePost")}
        </button>
      </div>
    </div>
  );
}

function ContentForm({ post, onSaved }: { post: PostDetail; onSaved: (p: PostDetail) => void }) {
  const { t } = useI18n();
  const [title, setTitle] = useState(post.title);
  const [body, setBody] = useState(post.body);
  const [state, setState] = useState<"" | "saving" | "saved">("");
  const [error, setError] = useState("");
  const dirty = title !== post.title || body !== post.body;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setState("saving");
    setError("");
    try {
      onSaved(await api.patch<PostDetail>(`/admin/posts/${post.id}`, { title, body }));
      setState("saved");
    } catch (err) {
      setError((err as ApiError).message);
      setState("");
    }
  }

  return (
    <section className="card padded">
      <form className="form" onSubmit={submit}>
        <label>
          {t("title")}
          <input value={title} maxLength={200} required onChange={(e) => setTitle(e.target.value)} />
        </label>
        <label>
          {t("text")}
          <textarea rows={6} value={body} maxLength={20000} onChange={(e) => setBody(e.target.value)} />
        </label>
        {error && <p className="form-error">{error}</p>}
        <div className="row">
          <button className="btn primary" disabled={!dirty || state === "saving"}>
            {t("save")}
          </button>
          {state === "saved" && !dirty && <span className="form-ok">{t("saved")}</span>}
        </div>
      </form>
    </section>
  );
}

function VideoSection({ post, onChange }: { post: PostDetail; onChange: (p: PostDetail) => void }) {
  const { t } = useI18n();
  const { config } = useApp();
  const [progress, setProgress] = useState<number | null>(null);
  const [error, setError] = useState("");
  const handle = useRef<UploadHandle<PostDetail> | null>(null);
  const v = post.video;

  async function onFile(file: File | undefined) {
    if (!file) return;
    setError("");
    if (config && file.size > config.max_video_bytes) {
      setError(`${formatBytes(file.size)} > ${formatBytes(config.max_video_bytes)}`);
      return;
    }
    setProgress(0);
    handle.current = upload<PostDetail>(`/admin/posts/${post.id}/video`, file, {}, setProgress);
    try {
      onChange(await handle.current.promise);
    } catch (e) {
      setError((e as ApiError).message);
    } finally {
      setProgress(null);
      handle.current = null;
    }
  }

  return (
    <section className="card padded">
      <h2>{t("video")}</h2>
      {v && (
        <div className="video-admin">
          <p>
            <span className={`status status-video-${v.status}`}>{t(`video_${v.status}` as MsgKey)}</span>{" "}
            <span className="muted">
              {v.original_filename} · {formatBytes(v.size_bytes)}
              {v.duration_seconds != null && ` · ${formatMinutes(v.duration_seconds, t("min"))}`}
            </span>
          </p>
          {v.error && <p className="form-error">{v.error}</p>}
          {v.status === "ready" && (
            <video
              controls
              preload="metadata"
              className="admin-video"
              poster={v.poster_asset_id ? mediaUrl(v.poster_asset_id) : undefined}
              src={mediaUrl(v.id)}
            />
          )}
        </div>
      )}
      {progress !== null ? (
        <div className="upload-progress">
          <progress max={1} value={progress} />
          <span>
            {t("uploading")} {Math.round(progress * 100)}%
          </span>
          <button className="link-button" onClick={() => handle.current?.abort()}>
            {t("cancel")}
          </button>
        </div>
      ) : (
        <label className="btn file-btn">
          {v ? t("replaceVideo") : t("uploadVideo")}
          <input
            type="file"
            accept="video/*,.mp4,.mov,.m4v,.webm,.mkv,.avi"
            onChange={(e) => {
              onFile(e.target.files?.[0]);
              e.target.value = "";
            }}
          />
        </label>
      )}
      {error && <p className="form-error">{error}</p>}
    </section>
  );
}

function AnalysisSection({ post, onChange }: { post: PostDetail; onChange: (p: PostDetail) => void }) {
  const { t, lang } = useI18n();
  const { config } = useApp();
  const [error, setError] = useState("");
  const [available, setAvailable] = useState<StepInfo[]>([]);
  const [selected, setSelected] = useState<string[]>([]);
  const [retranscribe, setRetranscribe] = useState(false);
  const a = post.analysis;
  const running = a?.status === "pending" || a?.status === "running";
  const canRun = post.video?.status === "ready" && !running && selected.length > 0;

  useEffect(() => {
    api
      .get<{ steps: StepInfo[]; defaults: string[] }>("/admin/analysis/steps")
      .then((r) => {
        setAvailable(r.steps);
        setSelected(r.defaults);
      })
      .catch(() => {});
  }, []);

  async function run() {
    setError("");
    try {
      onChange(await api.post<PostDetail>(`/admin/posts/${post.id}/analysis`, { steps: selected, retranscribe }));
      setRetranscribe(false);
    } catch (e) {
      setError((e as ApiError).message);
    }
  }

  const toggle = (name: string) =>
    setSelected((cur) => (cur.includes(name) ? cur.filter((n) => n !== name) : [...cur, name]));

  return (
    <section className="card padded">
      <h2>{t("analysis")}</h2>
      {config && !config.analysis_enabled && <p className="notice">{t("analysisDisabled")}</p>}

      {available.length > 0 && (
        <div className="step-picker">
          <div className="filter-title">{t("analysisSteps")}</div>
          {available.map((s) => (
            <label key={s.name} className="step-option">
              <input type="checkbox" checked={selected.includes(s.name)} onChange={() => toggle(s.name)} />
              <span>
                <strong>{s.title}</strong>{" "}
                <span className="muted small">
                  {s.provider}
                  {s.model && ` · ${s.model}`}
                </span>
                <br />
                <span className="muted small">{s.description}</span>
              </span>
            </label>
          ))}
          {selected.includes("transcribe") && (
            <label className="step-option small">
              <input type="checkbox" checked={retranscribe} onChange={(e) => setRetranscribe(e.target.checked)} />
              {t("retranscribe")}
            </label>
          )}
          <button className="btn" disabled={!canRun} onClick={run}>
            {t("runAnalysis")}
          </button>
        </div>
      )}
      {error && <p className="form-error">{error}</p>}

      {a ? (
        <>
          <h3>{t("lastRun")}</h3>
          <p>
            <span className={`status status-analysis-${a.status}`}>{t(`analysis_${a.status}` as MsgKey)}</span>{" "}
            <span className="muted">
              {a.completed_at && formatDate(a.completed_at, lang)}
              {a.input_tokens != null &&
                a.input_tokens + (a.output_tokens ?? 0) > 0 &&
                ` · ${a.input_tokens + (a.output_tokens ?? 0)} ${t("tokens")}`}
            </span>
          </p>
          {a.error && a.status !== "skipped" && <p className="form-error">{a.error}</p>}
          {a.steps && a.steps.length > 0 && (
            <table className="table steps-table">
              <thead>
                <tr>
                  <th>{t("step")}</th>
                  <th>{t("status")}</th>
                  <th />
                </tr>
              </thead>
              <tbody>
                {a.steps.map((s) => (
                  <tr key={s.name}>
                    <td>
                      <strong>{s.name}</strong>
                      <div className="muted small">
                        {s.provider}
                        {s.model && ` · ${s.model}`}
                      </div>
                    </td>
                    <td>
                      <span className={`status status-step-${s.status}`}>{t(`step_${s.status}` as MsgKey)}</span>
                      {s.duration_ms > 0 && <div className="muted small">{(s.duration_ms / 1000).toFixed(1)} s</div>}
                    </td>
                    <td className="small">
                      {s.note && <div>{s.note}</div>}
                      {s.error && <div className="form-error">{s.error}</div>}
                      {(s.usage?.input_tokens ?? 0) > 0 && (
                        <div className="muted">
                          {(s.usage.input_tokens ?? 0) + (s.usage.output_tokens ?? 0)} {t("tokens")}
                        </div>
                      )}
                      {s.output && (
                        <details>
                          <summary className="muted">JSON</summary>
                          <pre className="json">{JSON.stringify(s.output, null, 2)}</pre>
                        </details>
                      )}
                    </td>
                  </tr>
                ))}
              </tbody>
            </table>
          )}
          {a.result && (
            <>
              {a.result.summary && <p>{a.result.summary}</p>}
              <details>
                <summary>{t("rawResult")}</summary>
                <pre className="json">{JSON.stringify(a.result, null, 2)}</pre>
              </details>
            </>
          )}
          <TranscriptView postId={post.id} version={a.id + a.status} />
        </>
      ) : (
        <p className="muted">—</p>
      )}
    </section>
  );
}

function TranscriptView({ postId, version }: { postId: number; version: string }) {
  const { t } = useI18n();
  const [open, setOpen] = useState(false);
  const [data, setData] = useState<Transcript | null | undefined>(undefined);

  useEffect(() => setData(undefined), [version]);
  useEffect(() => {
    if (!open || data !== undefined) return;
    api
      .get<Transcript>(`/admin/posts/${postId}/transcript`)
      .then(setData)
      .catch(() => setData(null));
  }, [open, data, postId]);

  return (
    <details onToggle={(e) => setOpen((e.target as HTMLDetailsElement).open)}>
      <summary>{t("transcript")}</summary>
      {data === undefined && <p className="muted">{t("loading")}</p>}
      {data === null && <p className="muted">{t("noTranscript")}</p>}
      {data && (
        <div className="transcript">
          <p className="muted small">
            {data.provider} · {data.model} · {data.language}
          </p>
          {data.segments.length > 0 ? (
            data.segments.map((s, i) => (
              <p key={i}>
                <span className="timecode">{formatClock(s.start)}</span> {s.text}
              </p>
            ))
          ) : (
            <p>{data.text}</p>
          )}
        </div>
      )}
    </details>
  );
}

interface EditableTag {
  kind: TagKind;
  slug: string;
  name: string;
  primary: boolean;
}

const EDIT_KINDS: TagKind[] = ["focus", "body_area", "level", "pose", "other"];

function TagEditor({ post, onSaved }: { post: PostDetail; onSaved: (p: PostDetail) => void }) {
  const { t, lang } = useI18n();
  const { vocabulary, config, tagLabel } = useApp();
  const toEditable = (p: PostDetail): EditableTag[] =>
    p.tags.map(({ kind, slug, name, primary }) => ({ kind, slug, name, primary }));
  const [tags, setTags] = useState<EditableTag[]>(() => toEditable(post));
  const [dirty, setDirty] = useState(false);
  const [kind, setKind] = useState<TagKind>("focus");
  const [value, setValue] = useState("");
  const [known, setKnown] = useState<TagFacet[]>([]);
  const [error, setError] = useState("");

  // Pick up server-side changes (e.g. a finished analysis) unless editing.
  useEffect(() => {
    if (!dirty) setTags(toEditable(post));
  }, [post]);

  useEffect(() => {
    api.get<{ items: TagFacet[] }>("/admin/tags").then((r) => setKnown(r.items)).catch(() => {});
  }, []);

  const contentLang = config?.content_language ?? "ru";
  const origins: Record<string, string> = Object.fromEntries(
    post.tags.filter((x) => x.origin).map((x) => [x.kind + ":" + x.slug, x.origin!]),
  );
  const fixed = kind === "focus" || kind === "body_area" || kind === "level";
  const options = fixed && vocabulary ? vocabulary[kind as "focus" | "body_area" | "level"] : [];

  function update(next: EditableTag[]) {
    setTags(next);
    setDirty(true);
  }

  function add(e: FormEvent) {
    e.preventDefault();
    let tag: EditableTag | null = null;
    if (fixed) {
      const term = options.find((o) => o.slug === value);
      if (term) tag = { kind, slug: term.slug, name: contentLang === "en" ? term.en : term.ru, primary: false };
    } else if (value.trim()) {
      const existing = known.find((k) => k.kind === kind && k.name.toLowerCase() === value.trim().toLowerCase());
      tag = { kind, slug: existing?.slug ?? "", name: existing?.name ?? value.trim(), primary: false };
    }
    if (!tag) return;
    if (tags.some((x) => x.kind === tag!.kind && ((x.slug === tag!.slug && tag!.slug !== "") || x.name === tag!.name)))
      return;
    update([...tags, tag]);
    setValue("");
  }

  async function save() {
    setError("");
    try {
      const p = await api.put<PostDetail>(`/admin/posts/${post.id}/tags`, { tags });
      setDirty(false);
      setTags(toEditable(p));
      onSaved(p);
    } catch (e) {
      setError((e as ApiError).message);
    }
  }

  return (
    <section className="card padded">
      <h2>{t("tags")}</h2>
      <p className="muted small">{t("tagsHint")}</p>
      {EDIT_KINDS.map((k) => {
        const list = tags.filter((x) => x.kind === k);
        if (list.length === 0) return null;
        return (
          <div key={k} className="tag-edit-group">
            <div className="filter-title">{t(k)}</div>
            <div className="chips">
              {list.map((x) => (
                <span
                  key={x.kind + x.slug + x.name}
                  className={`chip chip-${x.kind}${x.primary ? " chip-primary" : ""}`}
                  title={origins[x.kind + ":" + x.slug] ? `${t("foundBy")}: ${origins[x.kind + ":" + x.slug]}` : undefined}
                >
                  {(k === "focus" || k === "body_area") && (
                    <button
                      className="chip-btn"
                      title={t("primary")}
                      onClick={() => update(tags.map((y) => (y === x ? { ...y, primary: !y.primary } : y)))}
                    >
                      {x.primary ? "★" : "☆"}
                    </button>
                  )}
                  {tagLabel(x.kind, x.slug, x.name)}
                  <button className="chip-btn" aria-label={t("delete")} onClick={() => update(tags.filter((y) => y !== x))}>
                    ×
                  </button>
                </span>
              ))}
            </div>
          </div>
        );
      })}

      <form className="inline-form" onSubmit={add}>
        <select
          value={kind}
          onChange={(e) => {
            setKind(e.target.value as TagKind);
            setValue("");
          }}
        >
          {EDIT_KINDS.map((k) => (
            <option key={k} value={k}>
              {t(k)}
            </option>
          ))}
        </select>
        {fixed ? (
          <select value={value} onChange={(e) => setValue(e.target.value)}>
            <option value="">—</option>
            {options.map((o) => (
              <option key={o.slug} value={o.slug}>
                {lang === "en" ? o.en : o.ru}
              </option>
            ))}
          </select>
        ) : (
          <>
            <input list="known-tags" value={value} maxLength={100} onChange={(e) => setValue(e.target.value)} />
            <datalist id="known-tags">
              {known
                .filter((k) => k.kind === kind)
                .map((k) => (
                  <option key={k.slug} value={k.name} />
                ))}
            </datalist>
          </>
        )}
        <button className="btn">{t("addTag")}</button>
      </form>
      {error && <p className="form-error">{error}</p>}
      <button className="btn primary" disabled={!dirty} onClick={save}>
        {t("save")}
      </button>
    </section>
  );
}

function ImagesSection({ post, onChange }: { post: PostDetail; onChange: (p: PostDetail) => void }) {
  const { t } = useI18n();
  const [progress, setProgress] = useState<string | null>(null);
  const [caption, setCaption] = useState("");
  const [error, setError] = useState("");

  async function onFiles(files: FileList | null) {
    if (!files?.length) return;
    setError("");
    const list = Array.from(files);
    try {
      for (let i = 0; i < list.length; i++) {
        setProgress(`${i + 1}/${list.length}`);
        const h = upload<PostDetail>(`/admin/posts/${post.id}/images`, list[i], { caption }, () => {});
        onChange(await h.promise);
      }
      setCaption("");
    } catch (e) {
      setError((e as ApiError).message);
    } finally {
      setProgress(null);
    }
  }

  async function remove(assetId: number) {
    try {
      onChange(await api.del<PostDetail>(`/admin/posts/${post.id}/images/${assetId}`));
    } catch (e) {
      setError((e as ApiError).message);
    }
  }

  return (
    <section className="card padded">
      <h2>{t("images")}</h2>
      {post.images.length > 0 && (
        <div className="gallery">
          {post.images.map((img) => (
            <figure key={img.asset.id}>
              <img src={mediaUrl(img.asset.id)} alt={img.caption} loading="lazy" />
              <figcaption>
                {img.caption}
                <button className="link-button danger-text" onClick={() => remove(img.asset.id)}>
                  {t("delete")}
                </button>
              </figcaption>
            </figure>
          ))}
        </div>
      )}
      <div className="inline-form">
        <input placeholder={t("caption")} value={caption} maxLength={500} onChange={(e) => setCaption(e.target.value)} />
        {progress ? (
          <span className="muted">
            {t("uploading")} {progress}
          </span>
        ) : (
          <label className="btn file-btn">
            {t("addImages")}
            <input
              type="file"
              accept="image/jpeg,image/png,image/webp,image/gif"
              multiple
              onChange={(e) => {
                onFiles(e.target.files);
                e.target.value = "";
              }}
            />
          </label>
        )}
      </div>
      {error && <p className="form-error">{error}</p>}
    </section>
  );
}
