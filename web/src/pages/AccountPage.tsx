import { useState, type FormEvent } from "react";
import { useNavigate } from "react-router-dom";
import { api, ApiError } from "../api";
import { useApp } from "../app-state";
import { useI18n } from "../i18n";

export default function AccountPage() {
  const { t } = useI18n();
  const { user, logout } = useApp();
  const navigate = useNavigate();
  const [current, setCurrent] = useState("");
  const [next, setNext] = useState("");
  const [msg, setMsg] = useState("");
  const [error, setError] = useState("");
  const [busy, setBusy] = useState(false);

  async function submit(e: FormEvent) {
    e.preventDefault();
    setBusy(true);
    setMsg("");
    setError("");
    try {
      await api.post("/me/password", { current_password: current, new_password: next });
      setMsg(t("passwordChanged"));
      setCurrent("");
      setNext("");
    } catch (err) {
      setError(err instanceof ApiError ? err.message : String(err));
    } finally {
      setBusy(false);
    }
  }

  return (
    <div className="narrow">
      <h1>{t("account")}</h1>
      <div className="card padded">
        <p>
          <strong>{user?.display_name || user?.email}</strong>
          <br />
          <span className="muted">{user?.email}</span>
        </p>
        <button
          className="btn"
          onClick={async () => {
            await logout();
            navigate("/login");
          }}
        >
          {t("signOut")}
        </button>
      </div>

      <div className="card padded">
        <h2>{t("changePassword")}</h2>
        <form onSubmit={submit} className="form">
          <label>
            {t("currentPassword")}
            <input
              type="password"
              autoComplete="current-password"
              required
              value={current}
              onChange={(e) => setCurrent(e.target.value)}
            />
          </label>
          <label>
            {t("newPassword")}
            <input
              type="password"
              autoComplete="new-password"
              minLength={8}
              required
              value={next}
              onChange={(e) => setNext(e.target.value)}
            />
          </label>
          {error && <p className="form-error">{error}</p>}
          {msg && <p className="form-ok">{msg}</p>}
          <button className="btn primary" disabled={busy}>
            {t("save")}
          </button>
        </form>
      </div>
    </div>
  );
}
