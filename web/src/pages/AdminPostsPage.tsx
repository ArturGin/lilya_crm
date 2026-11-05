import { useEffect, useState, type FormEvent } from "react";
import { Link, useNavigate, useSearchParams } from "react-router-dom";
import { api, ApiError, type PostCard, type PostDetail } from "../api";
import { useI18n, type MsgKey } from "../i18n";
import { formatDate, formatMinutes } from "../util";

const STATUSES = ["", "draft", "published", "archived"] as const;

export default function AdminPostsPage() {
  const { t, lang } = useI18n();
  const navigate = useNavigate();
  const [params, setParams] = useSearchParams();
  const status = params.get("status") ?? "";
  const [items, setItems] = useState<PostCard[] | null>(null);
  const [error, setError] = useState("");
  const [title, setTitle] = useState("");
  const [busy, setBusy] = useState(false);

  useEffect(() => {
    setItems(null);
    const p = new URLSearchParams({ limit: "100" });
    if (status) p.set("status", status);
    api
      .get<{ items: PostCard[] }>(`/admin/posts?${p}`)
      .then((r) => setItems(r.items))
      .catch((e) => setError(e.message));
  }, [status]);

  async function create(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    try {
      const p = await api.post<PostDetail>("/admin/posts", { title });
      navigate(`/admin/posts/${p.id}`);
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
      setBusy(false);
    }
  }

  return (
    <div>
      <h1>{t("posts")}</h1>
      <form className="inline-form card padded" onSubmit={create}>
        <input
          placeholder={t("title")}
          value={title}
          required
          maxLength={200}
          onChange={(e) => setTitle(e.target.value)}
        />
        <button className="btn primary" disabled={busy}>
          {t("newPost")}
        </button>
      </form>

      <div className="tabs">
        {STATUSES.map((s) => (
          <button
            key={s}
            className={`tab${status === s ? " active" : ""}`}
            onClick={() => setParams(s ? { status: s } : {}, { replace: true })}
          >
            {s ? t(s as MsgKey) : t("all")}
          </button>
        ))}
      </div>

      {error && <p className="form-error">{error}</p>}
      {!items && !error && <p className="muted">{t("loading")}</p>}
      {items && (
        <table className="table">
          <thead>
            <tr>
              <th>{t("title")}</th>
              <th>{t("status")}</th>
              <th>{t("video")}</th>
              <th>{t("analysis")}</th>
              <th>{t("updated")}</th>
            </tr>
          </thead>
          <tbody>
            {items.map((p) => (
              <tr key={p.id}>
                <td>
                  <Link to={`/admin/posts/${p.id}`}>{p.title}</Link>
                </td>
                <td>
                  <span className={`status status-${p.status}`}>{t(p.status as MsgKey)}</span>
                </td>
                <td>
                  {p.video_status ? t(`video_${p.video_status}` as MsgKey) : "—"}
                  {p.duration_seconds != null && (
                    <span className="muted"> · {formatMinutes(p.duration_seconds, t("min"))}</span>
                  )}
                </td>
                <td>{p.analysis_status ? t(`analysis_${p.analysis_status}` as MsgKey) : "—"}</td>
                <td className="muted">{formatDate(p.updated_at, lang)}</td>
              </tr>
            ))}
          </tbody>
        </table>
      )}
    </div>
  );
}
