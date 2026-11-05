import type { ReactNode } from "react";
import { Link, Navigate, NavLink, Route, Routes, useLocation } from "react-router-dom";
import { useApp } from "./app-state";
import { useI18n } from "./i18n";
import AccountPage from "./pages/AccountPage";
import AdminPostEditPage from "./pages/AdminPostEditPage";
import AdminPostsPage from "./pages/AdminPostsPage";
import LibraryPage from "./pages/LibraryPage";
import { LoginPage, RegisterPage } from "./pages/AuthPages";
import PostPage from "./pages/PostPage";

function RequireAuth({ children, admin }: { children: ReactNode; admin?: boolean }) {
  const { user, ready } = useApp();
  const { t } = useI18n();
  const loc = useLocation();
  if (!ready) return <div className="page-loading">{t("loading")}</div>;
  if (!user) return <Navigate to={`/login?next=${encodeURIComponent(loc.pathname + loc.search)}`} replace />;
  if (admin && user.role !== "admin") return <Navigate to="/" replace />;
  return <>{children}</>;
}

function Header() {
  const { user } = useApp();
  const { t, lang, setLang } = useI18n();
  return (
    <header className="topbar">
      <div className="topbar-inner">
        <Link to="/" className="brand">
          <img src="/favicon.svg" alt="" width={26} height={26} />
          {t("appName")}
        </Link>
        <nav className="nav">
          {user && (
            <NavLink to="/" end>
              {t("library")}
            </NavLink>
          )}
          {user?.role === "admin" && <NavLink to="/admin">{t("admin")}</NavLink>}
          {user && <NavLink to="/account">{t("account")}</NavLink>}
          <button className="link-button" onClick={() => setLang(lang === "ru" ? "en" : "ru")}>
            {t("language")}
          </button>
        </nav>
      </div>
    </header>
  );
}

export default function App() {
  return (
    <>
      <Header />
      <main className="container">
        <Routes>
          <Route path="/login" element={<LoginPage />} />
          <Route path="/register" element={<RegisterPage />} />
          <Route
            path="/"
            element={
              <RequireAuth>
                <LibraryPage />
              </RequireAuth>
            }
          />
          <Route
            path="/p/:id"
            element={
              <RequireAuth>
                <PostPage />
              </RequireAuth>
            }
          />
          <Route
            path="/account"
            element={
              <RequireAuth>
                <AccountPage />
              </RequireAuth>
            }
          />
          <Route
            path="/admin"
            element={
              <RequireAuth admin>
                <AdminPostsPage />
              </RequireAuth>
            }
          />
          <Route
            path="/admin/posts/:id"
            element={
              <RequireAuth admin>
                <AdminPostEditPage />
              </RequireAuth>
            }
          />
          <Route path="*" element={<Navigate to="/" replace />} />
        </Routes>
      </main>
    </>
  );
}
