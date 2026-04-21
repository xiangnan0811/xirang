import { useParams } from "react-router-dom";

export function DashboardDetailPage() {
  const { id } = useParams<{ id: string }>();
  return <div className="p-6">Dashboard detail (id: {id}) — coming in T11</div>;
}
