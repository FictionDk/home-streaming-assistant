import { useEffect } from "react";
import { EmptyState } from "../components/EmptyState";
import { Link } from "../components/Link";
import { replaceRoute } from "../router";

export function Redirect({ to }: { to: string }) {
  useEffect(() => {
    replaceRoute(to);
  }, [to]);
  return null;
}

export function NotFoundPage() {
  return (
    <section className="page">
      <h1>页面不存在</h1>
      <EmptyState text="请求的页面不存在。" />
      <p>
        <Link href="/sources">返回云端文件</Link>
      </p>
    </section>
  );
}
