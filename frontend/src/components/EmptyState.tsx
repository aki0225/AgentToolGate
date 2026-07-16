import type { ComponentProps, ReactNode } from "react";
import { type LucideIcon } from "lucide-react";
import { cn } from "@/lib/utils";
import { Button } from "./ui/button";

type EmptyStateProps = {
  icon?: LucideIcon;
  title: string;
  description: string;
  action?: ReactNode;
  className?: string;
};

export function EmptyState({ icon: Icon, title, description, action, className }: EmptyStateProps) {
  return (
    <div className={cn("grid place-items-center gap-4 rounded-[20px] border border-dashed border-border bg-white/[0.03] px-6 py-10 text-center", className)}>
      {Icon ? (
        <div className="grid h-14 w-14 place-items-center rounded-[18px] border border-primary/20 bg-primary/10 text-primary">
          <Icon className="h-6 w-6" />
        </div>
      ) : null}
      <div className="grid gap-2">
        <h3 className="text-base font-bold text-foreground">{title}</h3>
        <p className="max-w-xl text-sm leading-6 text-muted-foreground">{description}</p>
      </div>
      {action ? <div className="pt-1">{action}</div> : null}
    </div>
  );
}

export function EmptyStateButton(props: ComponentProps<typeof Button>) {
  return <Button {...props} />;
}
