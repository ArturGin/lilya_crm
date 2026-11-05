import { useEffect, useRef, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { api, ApiError, mediaUrl, type AnalysisResult, type Completion, type PostDetail } from "../api";
import { useApp } from "../app-state";
import { TagChips } from "../components/TagChips";
import { useI18n, type MsgKey } from "../i18n";
import { formatClock, formatDate, formatMinutes } from "../util";

export default function PostPage() {
  const { id } = useParams();
  const { t, lang } = useI18n();
  const { user } = useApp();
  const [post, setPost] = useState<PostDetail | null>(null);
  const [error, setError] = useState("");
  const videoRef = useRef<HTMLVideoElement>(null);

  useEffect(() => {
    setPost(null);
    setError("");
    api
      .get<PostDetail>(`/posts/${id}`)
      .then(setPost)
      .catch((e: ApiError) => setError(e.message));
  }, [id]);

  if (error) return <p className="form-error">{error}</p>;
  if (!post) return <p className="muted">{t("loading")}</p>;

  const seek = (sec: number) => {
    const v = videoRef.current;
    if (!v) return;
    v.currentTime = sec;
    v.play().catch(() => {});
    v.scrollIntoView({ behavior: "smooth", block: "center" });
  };

  const analysis = post.analysis?.status === "done" ? post.analysis.result : null;

  return (
    <article className="post">
      <div className="post-top">
        <Link to="/" className="muted">
          ← {t("back")}
        </Link>
        {user?.role === "admin" && <Link to={`/admin/posts/${post.id}`}>{t("admin")} →</Link>}
      </div>

      <VideoBlock post={post} videoRef={videoRef} />

      <header className="post-header">
        <div>
          <h1>{post.title}</h1>
          <p className="muted">
            {formatMinutes(post.duration_seconds, t("min"))}
            {post.published_at && ` · ${formatDate(post.published_at, lang)}`}
          </p>
        </div>
        <CompletionControl
          postId={post.id}
          completion={post.completion}
          onChange={(c) => setPost({ ...post, completion: c })}
        />
      </header>

      <TagChips tags={post.tags} kinds={["focus", "body_area", "level"]} />

      {post.body && (
        <section className="post-section">
          <h2>{t("aboutPractice")}</h2>
          <p className="post-body">{post.body}</p>
        </section>
      )}

      {analysis && <AnalysisView result={analysis} onSeek={seek} hasVideo={post.video?.status === "ready"} />}

      {post.images.length > 0 && (
        <section className="post-section">
          <h2>{t("images")}</h2>
          <div className="gallery">
            {post.images.map((img) => (
              <figure key={img.asset.id}>
                <a href={mediaUrl(img.asset.id)} target="_blank" rel="noreferrer">
                  <img src={mediaUrl(img.asset.id)} alt={img.caption} loading="lazy" />
                </a>
                {img.caption && <figcaption>{img.caption}</figcaption>}
              </figure>
            ))}
          </div>
        </section>
      )}
    </article>
  );
}

function VideoBlock({ post, videoRef }: { post: PostDetail; videoRef: React.RefObject<HTMLVideoElement | null> }) {
  const { t } = useI18n();
  const v = post.video;
  if (!v) return null;
  if (v.status === "ready") {
    return (
      <div className="video-wrap">
        <video
          ref={videoRef}
          controls
          playsInline
          preload="metadata"
          poster={v.poster_asset_id ? mediaUrl(v.poster_asset_id) : undefined}
          src={mediaUrl(v.id)}
        />
      </div>
    );
  }
  return (
    <div className="video-wrap video-pending">
      <p>{v.status === "failed" ? t("videoFailed") : t("videoProcessing")}</p>
    </div>
  );
}

function CompletionControl({
  postId,
  completion,
  onChange,
}: {
  postId: number;
  completion: Completion;
  onChange: (c: Completion) => void;
}) {
  const { t, lang } = useI18n();
  const [busy, setBusy] = useState(false);

  async function run(fn: () => Promise<{ completion: Completion }>) {
    setBusy(true);
    try {
      const r = await fn();
      onChange(r.completion);
    } finally {
      setBusy(false);
    }
  }
  const mark = () => run(() => api.post(`/posts/${postId}/completions`));
  const undo = () => run(() => api.del(`/posts/${postId}/completions/latest`));

  if (completion.count === 0) {
    return (
      <button className="btn primary" onClick={mark} disabled={busy}>
        {t("markDone")}
      </button>
    );
  }
  return (
    <div className="completion">
      <div className="completion-status">
        ✓ {t("doneOn")} {formatDate(completion.last_completed_at, lang)}
        {completion.count > 1 && (
          <span className="muted">
            {" "}
            · {completion.count} {t("doneTimes")}
          </span>
        )}
      </div>
      <div className="completion-actions">
        <button className="btn small" onClick={mark} disabled={busy}>
          {t("doAgain")}
        </button>
        <button className="link-button" onClick={undo} disabled={busy}>
          {t("undo")}
        </button>
      </div>
    </div>
  );
}

function AnalysisView({
  result,
  onSeek,
  hasVideo,
}: {
  result: AnalysisResult;
  onSeek: (sec: number) => void;
  hasVideo: boolean;
}) {
  const { t, lang } = useI18n();
  const { tagLabel } = useApp();
  return (
    <section className="post-section analysis">
      <h2>{t("analysis")}</h2>
      {result.summary && <p>{result.summary}</p>}
      <dl className="facts">
        {result.practice_type && (
          <>
            <dt>{t("practiceType")}</dt>
            <dd>{result.practice_type}</dd>
          </>
        )}
        {result.intensity && (
          <>
            <dt>{t("intensity")}</dt>
            <dd>{t(`intensity_${result.intensity}` as MsgKey)}</dd>
          </>
        )}
        {result.props.length > 0 && (
          <>
            <dt>{t("props")}</dt>
            <dd>{result.props.join(", ")}</dd>
          </>
        )}
        {result.cautions && (
          <>
            <dt>{t("cautions")}</dt>
            <dd>{result.cautions}</dd>
          </>
        )}
      </dl>

      {result.poses.length > 0 && (
        <>
          <h3>{t("posesInPractice")}</h3>
          <ol className="pose-list">
            {result.poses.map((p) => (
              <li key={p.slug}>
                {hasVideo ? (
                  <button className="timecode" onClick={() => onSeek(p.start_seconds)}>
                    {formatClock(p.start_seconds)}
                  </button>
                ) : (
                  <span className="timecode">{formatClock(p.start_seconds)}</span>
                )}
                <div>
                  <Link to={`/?tag=${encodeURIComponent(`pose:${p.slug}`)}`} className="pose-name">
                    {lang === "en" ? p.name_en || p.name : p.name || p.name_en}
                  </Link>
                  {p.sanskrit && <span className="muted"> · {p.sanskrit}</span>}
                  {p.body_areas && p.body_areas.length > 0 && (
                    <div className="muted small">{p.body_areas.map((a) => tagLabel("body_area", a, a)).join(", ")}</div>
                  )}
                </div>
              </li>
            ))}
          </ol>
        </>
      )}
    </section>
  );
}
