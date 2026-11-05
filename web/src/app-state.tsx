import { createContext, useCallback, useContext, useEffect, useState, type ReactNode } from "react";
import { api, ApiError, type PublicConfig, type TagKind, type User, type Vocabulary } from "./api";
import { useI18n } from "./i18n";

interface AppState {
  user: User | null;
  config: PublicConfig | null;
  vocabulary: Vocabulary | null;
  ready: boolean;
  setUser: (u: User | null) => void;
  logout: () => Promise<void>;
  /** Display name for a tag: fixed vocabularies follow the UI language. */
  tagLabel: (kind: TagKind, slug: string, fallback: string) => string;
}

const Ctx = createContext<AppState | null>(null);

export function AppStateProvider({ children }: { children: ReactNode }) {
  const { lang } = useI18n();
  const [user, setUser] = useState<User | null>(null);
  const [config, setConfig] = useState<PublicConfig | null>(null);
  const [vocabulary, setVocabulary] = useState<Vocabulary | null>(null);
  const [ready, setReady] = useState(false);

  useEffect(() => {
    api.get<PublicConfig>("/config").then(setConfig).catch(() => {});
    api
      .get<{ user: User }>("/me")
      .then((r) => setUser(r.user))
      .catch((e) => {
        if (!(e instanceof ApiError && e.status === 401)) console.error(e);
      })
      .finally(() => setReady(true));
  }, []);

  useEffect(() => {
    if (!user) return;
    api.get<Vocabulary>("/vocabulary").then(setVocabulary).catch(() => {});
  }, [user]);

  const logout = useCallback(async () => {
    try {
      await api.post("/auth/logout");
    } finally {
      setUser(null);
    }
  }, []);

  const tagLabel = useCallback(
    (kind: TagKind, slug: string, fallback: string) => {
      if (vocabulary && (kind === "focus" || kind === "body_area" || kind === "level")) {
        const term = vocabulary[kind].find((t) => t.slug === slug);
        if (term) return lang === "en" ? term.en : term.ru;
      }
      return fallback;
    },
    [vocabulary, lang],
  );

  return (
    <Ctx.Provider value={{ user, config, vocabulary, ready, setUser, logout, tagLabel }}>{children}</Ctx.Provider>
  );
}

export function useApp(): AppState {
  const v = useContext(Ctx);
  if (!v) throw new Error("AppStateProvider missing");
  return v;
}
