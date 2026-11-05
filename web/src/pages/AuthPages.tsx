import { useState, type FormEvent } from "react";
import { Link, Navigate, useNavigate, useSearchParams } from "react-router-dom";
import { api, ApiError, type User } from "../api";
import { useApp } from "../app-state";
import { useI18n } from "../i18n";

function safeNext(next: string | null): string {
  // Only allow same-site relative paths (prevents open redirects).
  return next && next.startsWith("/") && !next.startsWith("//") ? next : "/";
}

export function LoginPage() {
  const { t } = useI18n();
  const { user, setUser, config } = useApp();
  const [params] = useSearchParams();
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);
  const next = safeNext(params.get("next"));

  if (user) return <Navigate to={next} replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api.post<{ user: User }>("/auth/login", { email, password });
      setUser(r.user);
      navigate(next, { replace: true });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="auth-card card">
      <h1>{t("signIn")}</h1>
      <form onSubmit={submit} className="form">
        <label>
          {t("email")}
          <input type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
        <label>
          {t("password")}
          <input
            type="password"
            autoComplete="current-password"
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {error && <p className="form-error">{error}</p>}
        <button className="btn primary" disabled={busy}>
          {t("signIn")}
        </button>
      </form>
      {config?.allow_signup && (
        <p className="muted">
          {t("noAccount")} <Link to="/register">{t("register")}</Link>
        </p>
      )}
    </div>
  );
}

export function RegisterPage() {
  const { t } = useI18n();
  const { user, setUser, config } = useApp();
  const navigate = useNavigate();
  const [email, setEmail] = useState("");
  const [name, setName] = useState("");
  const [password, setPassword] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  if (user) return <Navigate to="/" replace />;
  if (config && !config.allow_signup) return <Navigate to="/login" replace />;

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setError("");
    try {
      const r = await api.post<{ user: User }>("/auth/register", { email, password, display_name: name });
      setUser(r.user);
      navigate("/", { replace: true });
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="auth-card card">
      <h1>{t("createAccount")}</h1>
      <form onSubmit={submit} className="form">
        <label>
          {t("name")}
          <input autoComplete="name" value={name} maxLength={100} onChange={(e) => setName(e.target.value)} />
        </label>
        <label>
          {t("email")}
          <input type="email" autoComplete="email" required value={email} onChange={(e) => setEmail(e.target.value)} />
        </label>
        <label>
          {t("password")}
          <input
            type="password"
            autoComplete="new-password"
            minLength={8}
            required
            value={password}
            onChange={(e) => setPassword(e.target.value)}
          />
        </label>
        {error && <p className="form-error">{error}</p>}
        <button className="btn primary" disabled={busy}>
          {t("createAccount")}
        </button>
      </form>
      <p className="muted">
        {t("haveAccount")} <Link to="/login">{t("signIn")}</Link>
      </p>
    </div>
  );
}
