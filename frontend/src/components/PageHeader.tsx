import type { ReactNode } from "react";
import type { LucideIcon } from "lucide-react";
import { cn } from "../lib/utils";

type PageHeaderProps = {
  kicker: ReactNode;
  title: ReactNode;
  description: ReactNode;
  icon?: LucideIcon;
  className?: string;
};

export function PageHeader({ kicker, title, description, icon: Icon, className }: PageHeaderProps) {
  return (
    <header
      className={cn(
        "rounded-[24px] border border-border/80 bg-[linear-gradient(135deg,rgba(12,20,35,0.92),rgba(12,20,35,0.68)),radial-gradient(circle_at_top_right,rgba(94,234,212,0.10),transparent_38%)] px-5 py-4 shadow-[0_14px_34px_rgba(2,6,23,0.30)] md:px-6",
        className,
      )}
    >
      <div className="max-w-3xl">
        <div className="inline-flex items-center gap-2 text-xs font-bold uppercase tracking-[0.18em] text-primary">
          {Icon ? <Icon className="h-4 w-4" /> : null}
          {kicker}
        </div>
        <h1 className="mt-1.5 text-2xl font-bold tracking-tight text-foreground md:text-3xl">{title}</h1>
        <p className="mt-2 max-w-3xl text-sm leading-5 text-muted-foreground">{description}</p>
      </div>
    </header>
  );
}
