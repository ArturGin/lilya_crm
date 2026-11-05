import { useEffect, useMemo, useState } from "react";
import { Link, useSearchParams } from "react-router-dom";
import { api, mediaUrl, type PostCard, type TagFacet, type TagKind } from "../api";
import { useApp } from "../app-state";
import { TagChips } from "../components/TagChips";
import { useI18n } from "../i18n";
import { DURATION_BUCKETS, durationLabel, formatMinutes } from "../util";

const FACET_KINDS: TagKind[] = ["focus", "body_area", "level", "pose"];
const PAGE_SIZE = 24;

export default function LibraryPage() {
  const { t } = useI18n();
  const [params, setParams] = useSearchParams();
  const [facets, setFacets] = useState<TagFacet[]>([]);
  const [items, setItems] = useState<PostCard[]>([]);
  const [total, setTotal] = useState(0);
  const [loading, setLoading] = useState(true);
  const [error, setError] = useState("");
  const [query, setQuery] = useState(params.get("q") ?? "");

  const selectedTags = params.getAll("tag");
  const dur = params.get("dur") ?? "";
  const done = params.get("done") ?? "";
  const q = params.get("q") ?? "";
  const hasFilters = selectedTags.length > 0 || dur || done || q;

  useEffect(() => {
    api.get<{ items: TagFacet[] }>("/tags").then((r) => setFacets(r.items)).catch(() => {});
  }, []);

  // Debounce the search box into the URL.
  useEffect(() => {
    const id = setTimeout(() => {
      if (query.trim() === q) return;
      const next = new URLSearchParams(params);
      if (query.trim()) next.set("q", query.trim());
      else next.delete("q");
      setParams(next, { replace: true });
    }, 300);
    return () => clearTimeout(id);
  }, [query]);

  const apiQuery = useMemo(() => {
    const p = new URLSearchParams();
    if (q) p.set("q", q);
    selectedTags.forEach((tag) => p.append("tag", tag));
    const bucket = DURATION_BUCKETS.find((b) => b.id === dur);
    if (bucket?.min !== undefined) p.set("min_duration", String(bucket.min));
    if (bucket?.max !== undefined) p.set("max_duration", String(bucket.max));
    if (done) p.set("done", done);
    return p;
  }, [params]);

  useEffect(() => {
    let cancelled = false;
    setLoading(true);
    setError("");
    const p = new URLSearchParams(apiQuery);
    p.set("limit", String(PAGE_SIZE));
    api
      .get<{ items: PostCard[]; total: number }>(`/posts?${p}`)
      .then((r) => {
        if (cancelled) return;
        setItems(r.items);
        setTotal(r.total);
      })
      .catch((e) => !cancelled && setError(e.message))
      .finally(() => !cancelled && setLoading(false));
    return () => {
      cancelled = true;
    };
  }, [apiQuery]);

  function loadMore() {
    const p = new URLSearchParams(apiQuery);
    p.set("limit", String(PAGE_SIZE));
    p.set("offset", String(items.length));
    api.get<{ items: PostCard[]; total: number }>(`/posts?${p}`).then((r) => setItems((prev) => [...prev, ...r.items]));
  }

  function toggleParam(key: string, value: string, multi = false) {
    const next = new URLSearchParams(params);
    if (multi) {
      const all = next.getAll(key);
      next.delete(key);
      const updated = all.includes(value) ? all.filter((v) => v !== value) : [...all, value];
      updated.forEach((v) => next.append(key, v));
    } else if (next.get(key) === value) {
      next.delete(key);
    } else {
      next.set(key, value);
    }
    setParams(next, { replace: true });
  }

  function clearAll() {
    setQuery("");
    setParams(new URLSearchParams(), { replace: true });
  }

  return (
    <div className="library">
      <div className="search-row">
        <input
          type="search"
          className="search-input"
          placeholder={t("searchPlaceholder")}
          value={query}
          onChange={(e) => setQuery(e.target.value)}
          aria-label={t("search")}
        />
      </div>

      <section className="filters">
        <FilterGroup title={t("duration")}>
          {DURATION_BUCKETS.map((b) => (
            <FilterChip key={b.id} active={dur === b.id} onClick={() => toggleParam("dur", b.id)}>
              {durationLabel(b.id, t("min"))}
            </FilterChip>
          ))}
        </FilterGroup>
        <FilterGroup title={t("status")}>
          <FilterChip active={done === "false"} onClick={() => toggleParam("done", "false")}>
            {t("notDone")}
          </FilterChip>
          <FilterChip active={done === "true"} onClick={() => toggleParam("done", "true")}>
            {t("done")}
          </FilterChip>
        </FilterGroup>
        {FACET_KINDS.map((kind) => (
          <FacetGroup
            key={kind}
            kind={kind}
            facets={facets.filter((f) => f.kind === kind)}
            selected={selectedTags}
            onToggle={(tag) => toggleParam("tag", tag, true)}
          />
        ))}
        {hasFilters && (
          <button className="link-button" onClick={clearAll}>
            {t("clearFilters")}
          </button>
        )}
      </section>

      {error && <p className="form-error">{error}</p>}
      {!loading && !error && (
        <p className="muted results-count">
          {items.length === 0 ? (hasFilters ? t("nothingFound") : t("emptyLibrary")) : `${t("results")}: ${total}`}
        </p>
      )}

      <div className="grid">
        {items.map((p) => (
          <PostCardView key={p.id} post={p} />
        ))}
      </div>
      {loading && <p className="muted">{t("loading")}</p>}
      {!loading && items.length < total && (
        <div className="center">
          <button className="btn" onClick={loadMore}>
            {t("loadMore")}
          </button>
        </div>
      )}
    </div>
  );
}

function FilterGroup({ title, children }: { title: string; children: React.ReactNode }) {
  return (
    <div className="filter-group">
      <div className="filter-title">{title}</div>
      <div className="chips chips-scroll">{children}</div>
    </div>
  );
}

function FilterChip({ active, onClick, children }: { active: boolean; onClick: () => void; children: React.ReactNode }) {
  return (
    <button type="button" className={`chip filter-chip${active ? " active" : ""}`} aria-pressed={active} onClick={onClick}>
      {children}
    </button>
  );
}

function FacetGroup({
  kind,
  facets,
  selected,
  onToggle,
}: {
  kind: TagKind;
  facets: TagFacet[];
  selected: string[];
  onToggle: (tag: string) => void;
}) {
  const { t } = useI18n();
  const { tagLabel } = useApp();
  const [expanded, setExpanded] = useState(false);
  if (facets.length === 0) return null;
  const limit = 12;
  const shown = expanded
    ? facets
    : facets.filter((f, i) => i < limit || selected.includes(`${f.kind}:${f.slug}`));
  return (
    <FilterGroup title={t(kind)}>
      {shown.map((f) => {
        const key = `${f.kind}:${f.slug}`;
        return (
          <FilterChip key={key} active={selected.includes(key)} onClick={() => onToggle(key)}>
            {tagLabel(f.kind, f.slug, f.name)} <span className="chip-count">{f.count}</span>
          </FilterChip>
        );
      })}
      {facets.length > limit && (
        <button type="button" className="link-button" onClick={() => setExpanded(!expanded)}>
          {expanded ? t("showLess") : `${t("showAll")} (${facets.length})`}
        </button>
      )}
    </FilterGroup>
  );
}

function PostCardView({ post }: { post: PostCard }) {
  const { t } = useI18n();
  return (
    <Link to={`/p/${post.id}`} className="post-card card">
      <div className="thumb">
        {post.poster_asset_id ? (
          <img src={mediaUrl(post.poster_asset_id)} alt="" loading="lazy" />
        ) : (
          <div className="thumb-placeholder" />
        )}
        {post.duration_seconds != null && (
          <span className="badge duration">{formatMinutes(post.duration_seconds, t("min"))}</span>
        )}
        {post.completion.count > 0 && <span className="badge done">✓ {t("done")}</span>}
      </div>
      <div className="post-card-body">
        <h3>{post.title}</h3>
        <TagChips tags={post.tags} kinds={["focus", "body_area"]} limit={4} />
      </div>
    </Link>
  );
}
