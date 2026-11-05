export function formatClock(sec: number): string {
  const s = Math.max(0, Math.floor(sec));
  const h = Math.floor(s / 3600);
  const m = Math.floor((s % 3600) / 60);
  const r = s % 60;
  const pad = (n: number) => String(n).padStart(2, "0");
  return h > 0 ? `${h}:${pad(m)}:${pad(r)}` : `${m}:${pad(r)}`;
}

export function formatMinutes(sec: number | null | undefined, minLabel: string): string {
  if (sec == null) return "";
  return `${Math.max(1, Math.round(sec / 60))} ${minLabel}`;
}

export function formatDate(iso: string | null | undefined, lang: string): string {
  if (!iso) return "";
  return new Date(iso).toLocaleDateString(lang === "en" ? "en-GB" : "ru-RU", {
    day: "numeric",
    month: "long",
    year: "numeric",
  });
}

export function formatBytes(n: number): string {
  if (n < 1024) return `${n} B`;
  const units = ["KB", "MB", "GB"];
  let v = n / 1024;
  let i = 0;
  while (v >= 1024 && i < units.length - 1) {
    v /= 1024;
    i++;
  }
  return `${v.toFixed(v < 10 ? 1 : 0)} ${units[i]}`;
}

/** Duration filter buckets (seconds). Ids are used in the page URL. */
export const DURATION_BUCKETS = [
  { id: "0-15", min: undefined, max: 15 * 60 },
  { id: "15-30", min: 15 * 60, max: 30 * 60 },
  { id: "30-45", min: 30 * 60, max: 45 * 60 },
  { id: "45-60", min: 45 * 60, max: 60 * 60 },
  { id: "60+", min: 60 * 60, max: undefined },
] as const;

export function durationLabel(id: string, minLabel: string): string {
  switch (id) {
    case "0-15":
      return `≤ 15 ${minLabel}`;
    case "60+":
      return `60+ ${minLabel}`;
    default:
      return `${id.replace("-", "–")} ${minLabel}`;
  }
}
