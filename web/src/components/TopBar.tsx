import { Link } from "./Link";

const NAV_ITEMS = [
  { href: "/sources", label: "云端" },
  { href: "/downloads", label: "任务" },
  { href: "/library", label: "本地" },
  { href: "/settings", label: "设置" },
];

export function TopBar({
  pathname,
  user,
  onLogout,
}: {
  pathname: string;
  user: string;
  onLogout: () => void;
}) {
  return (
    <header className="topbar">
      <div className="topbar-inner">
        <Link href="/sources" className="brand">
          家庭影音助手
        </Link>
        <nav className="nav">
          {NAV_ITEMS.map((item) => {
            const active =
              pathname === item.href || pathname.startsWith(`${item.href}/`);
            return (
              <Link key={item.href} href={item.href} className={active ? "nav-link active" : "nav-link"}>
                {item.label}
              </Link>
            );
          })}
        </nav>
        <div className="topbar-user">
          <span className="user-name">{user}</span>
          <button type="button" className="btn btn-ghost" onClick={onLogout}>
            退出
          </button>
        </div>
      </div>
    </header>
  );
}
