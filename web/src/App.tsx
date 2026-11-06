import { useEffect, useState } from "react";
import * as api from "./api";
import { TopBar } from "./components/TopBar";
import { LoginPage } from "./pages/LoginPage";
import { DownloadsPage } from "./pages/DownloadsPage";
import { DownloadDetailPage } from "./pages/DownloadDetailPage";
import { LibraryPage } from "./pages/LibraryPage";
import { NotFoundPage, Redirect } from "./pages/MiscPages";
import { SettingsPage } from "./pages/SettingsPage";
import { SourcesPage } from "./pages/SourcesPage";
import { usePathname } from "./router";

type AuthState = { status: "loading" } | { status: "anon" } | { status: "authed"; user: string };

function Route({ pathname }: { pathname: string }) {
  if (pathname === "/" || pathname === "") return <Redirect to="/sources" />;
  if (pathname === "/sources") return <SourcesPage />;
  if (pathname === "/downloads") return <DownloadsPage />;
  const detail = /^\/downloads\/([^/]+)$/.exec(pathname);
  if (detail !== null) return <DownloadDetailPage id={decodeURIComponent(detail[1])} />;
  if (pathname === "/library") return <LibraryPage />;
  if (pathname === "/settings") return <SettingsPage />;
  return <NotFoundPage />;
}

export default function App() {
  const [auth, setAuth] = useState<AuthState>({ status: "loading" });
  const pathname = usePathname();

  useEffect(() => {
    api.setUnauthorizedHandler(() => {
      api.clearSession();
      setAuth({ status: "anon" });
    });
    api
      .fetchSession()
      .then((session) => {
        api.storeSession(session);
        setAuth({ status: "authed", user: session.user });
      })
      .catch(() => {
        setAuth({ status: "anon" });
      });
  }, []);

  const handleLogout = async () => {
    try {
      await api.logout();
    } finally {
      api.clearSession();
      setAuth({ status: "anon" });
    }
  };

  if (auth.status === "loading") {
    return <div className="loading full-page">加载中…</div>;
  }

  if (auth.status === "anon") {
    return (
      <LoginPage
        onAuthorized={(session) => {
          api.storeSession(session);
          setAuth({ status: "authed", user: session.user });
        }}
      />
    );
  }

  return (
    <div className="app">
      <TopBar
        pathname={pathname}
        user={auth.user}
        onLogout={() => void handleLogout()}
      />
      <main className="main">
        <Route pathname={pathname} />
      </main>
    </div>
  );
}
