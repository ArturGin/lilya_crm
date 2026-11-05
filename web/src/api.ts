// Thin client for the /api/v1 JSON API. Every other frontend (Telegram
// mini-app, mobile) talks to the same endpoints; see README "API".

export type Role = "member" | "admin";

export interface User {
  id: number;
  email: string;
  display_name: string;
  role: Role;
  created_at: string;
}

export type TagKind = "pose" | "body_area" | "focus" | "level" | "other";

export interface PostTag {
  kind: TagKind;
  slug: string;
  name: string;
  primary: boolean;
  source: "ai" | "manual";
  /** Analysis steps that produced an AI tag, e.g. "keywords,claude" (admin only). */
  origin?: string;
}

export interface TagFacet {
  kind: TagKind;
  slug: string;
  name: string;
  count: number;
}

export interface Asset {
  id: number;
  kind: "video" | "image";
  original_filename: string;
  content_type: string;
  size_bytes: number;
  status: "uploaded" | "processing" | "ready" | "failed";
  error?: string;
  duration_seconds?: number;
  width?: number;
  height?: number;
  poster_asset_id?: number;
  created_at: string;
}

export interface Completion {
  count: number;
  last_completed_at: string | null;
}

export interface Pose {
  slug: string;
  name: string;
  name_en: string;
  sanskrit: string;
  start_seconds: number;
  body_areas: string[] | null;
  sources?: string[];
}

export interface AnalysisResult {
  summary: string;
  practice_type: string;
  level: string;
  intensity: "low" | "moderate" | "high" | "";
  focus: { slug: string; primary: boolean }[];
  body_areas: { slug: string; primary: boolean }[];
  poses: Pose[];
  props: string[];
  cautions: string;
}

export interface StepRecord {
  name: string;
  provider: string;
  model?: string;
  status: "pending" | "running" | "done" | "skipped" | "failed";
  note?: string;
  error?: string;
  started_at: string;
  duration_ms: number;
  usage: { input_tokens?: number; output_tokens?: number };
  output?: AnalysisResult;
}

export interface StepInfo {
  name: string;
  title: string;
  description: string;
  provider: string;
  model?: string;
  uses: string[];
  produces: string[];
}

export interface Transcript {
  id: number;
  provider: string;
  model: string;
  language: string;
  text: string;
  segments: { start: number; end: number; text: string }[];
  created_at: string;
}

export interface Analysis {
  id: number;
  status: "pending" | "running" | "done" | "failed" | "skipped";
  provider: string;
  model: string;
  result: AnalysisResult | null;
  steps?: StepRecord[];
  requested_steps?: string[];
  error?: string;
  input_tokens?: number;
  output_tokens?: number;
  created_at: string;
  completed_at: string | null;
}

export type PostStatus = "draft" | "published" | "archived";

export interface PostDetail {
  id: number;
  title: string;
  body: string;
  status: PostStatus;
  duration_seconds: number | null;
  published_at: string | null;
  created_at: string;
  updated_at: string;
  video: Asset | null;
  images: { asset: Asset; caption: string; position: number }[];
  tags: PostTag[];
  analysis: Analysis | null;
  completion: Completion;
}

export interface PostCard {
  id: number;
  title: string;
  status: PostStatus;
  duration_seconds: number | null;
  published_at: string | null;
  updated_at: string;
  poster_asset_id: number | null;
  video_status: Asset["status"] | null;
  analysis_status: Analysis["status"] | null;
  tags: PostTag[];
  completion: Completion;
}

export interface Term {
  slug: string;
  ru: string;
  en: string;
}

export interface Vocabulary {
  body_area: Term[];
  focus: Term[];
  level: Term[];
}

export interface PublicConfig {
  allow_signup: boolean;
  analysis_enabled: boolean;
  content_language: "ru" | "en";
  max_video_bytes: number;
  max_image_bytes: number;
}

export class ApiError extends Error {
  constructor(
    public status: number,
    public code: string,
    message: string,
  ) {
    super(message);
  }
}

const BASE = "/api/v1";

async function parseError(res: Response): Promise<ApiError> {
  try {
    const body = await res.json();
    return new ApiError(res.status, body?.error?.code ?? "error", body?.error?.message ?? res.statusText);
  } catch {
    return new ApiError(res.status, "error", res.statusText);
  }
}

export async function request<T>(method: string, path: string, body?: unknown): Promise<T> {
  const headers: Record<string, string> = { "X-Requested-With": "fetch" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const res = await fetch(BASE + path, {
    method,
    headers,
    credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (!res.ok) throw await parseError(res);
  if (res.status === 204) return undefined as T;
  return (await res.json()) as T;
}

export const api = {
  get: <T>(path: string) => request<T>("GET", path),
  post: <T>(path: string, body?: unknown) => request<T>("POST", path, body ?? {}),
  patch: <T>(path: string, body: unknown) => request<T>("PATCH", path, body),
  put: <T>(path: string, body: unknown) => request<T>("PUT", path, body),
  del: <T>(path: string) => request<T>("DELETE", path),
};

export interface UploadHandle<T> {
  promise: Promise<T>;
  abort: () => void;
}

/** Multipart upload with progress (fetch has no upload progress events). */
export function upload<T>(
  path: string,
  file: File,
  fields: Record<string, string>,
  onProgress: (fraction: number) => void,
): UploadHandle<T> {
  const xhr = new XMLHttpRequest();
  const promise = new Promise<T>((resolve, reject) => {
    const form = new FormData();
    for (const [k, v] of Object.entries(fields)) form.append(k, v);
    form.append("file", file); // file last: the server streams it
    xhr.open("POST", BASE + path);
    xhr.setRequestHeader("X-Requested-With", "fetch");
    xhr.upload.onprogress = (e) => e.lengthComputable && onProgress(e.loaded / e.total);
    xhr.onload = () => {
      let body: any = null;
      try {
        body = JSON.parse(xhr.responseText);
      } catch {
        /* empty */
      }
      if (xhr.status >= 200 && xhr.status < 300) resolve(body as T);
      else reject(new ApiError(xhr.status, body?.error?.code ?? "error", body?.error?.message ?? xhr.statusText));
    };
    xhr.onerror = () => reject(new ApiError(0, "network", "network error"));
    xhr.onabort = () => reject(new ApiError(0, "aborted", "upload cancelled"));
    xhr.send(form);
  });
  return { promise, abort: () => xhr.abort() };
}

export const mediaUrl = (id: number) => `${BASE}/media/${id}`;
