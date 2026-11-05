import { Link } from "react-router-dom";
import type { PostTag, TagKind } from "../api";
import { useApp } from "../app-state";

const ORDER: TagKind[] = ["focus", "body_area", "level", "pose", "other"];

export function sortTags(tags: PostTag[]): PostTag[] {
  return [...tags].sort(
    (a, b) => ORDER.indexOf(a.kind) - ORDER.indexOf(b.kind) || Number(b.primary) - Number(a.primary),
  );
}

/** Tag chips; each links to the library filtered by that tag. */
export function TagChips({ tags, kinds, limit }: { tags: PostTag[]; kinds?: TagKind[]; limit?: number }) {
  const { tagLabel } = useApp();
  let list = sortTags(tags).filter((t) => !kinds || kinds.includes(t.kind));
  const hidden = limit && list.length > limit ? list.length - limit : 0;
  if (limit) list = list.slice(0, limit);
  return (
    <div className="chips">
      {list.map((t) => (
        <Link
          key={t.kind + t.slug}
          to={`/?tag=${encodeURIComponent(`${t.kind}:${t.slug}`)}`}
          className={`chip chip-${t.kind}${t.primary ? " chip-primary" : ""}`}
          onClick={(e) => e.stopPropagation()}
        >
          {tagLabel(t.kind, t.slug, t.name)}
        </Link>
      ))}
      {hidden > 0 && <span className="chip chip-more">+{hidden}</span>}
    </div>
  );
}
