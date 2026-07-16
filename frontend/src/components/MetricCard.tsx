import { Card } from "./ui/card";

export function MetricCard({
  label,
  value,
  hint,
}: {
  label: string;
  value: string | number;
  hint?: string;
}) {
  return (
    <Card className="rounded-[20px] p-[18px]">
      <span className="text-xs uppercase tracking-wider text-muted-foreground">{label}</span>
      <strong className="mt-2 block text-3xl font-bold text-foreground">{value}</strong>
      {hint && <span className="mt-1 block text-sm text-muted-foreground">{hint}</span>}
    </Card>
  );
}
