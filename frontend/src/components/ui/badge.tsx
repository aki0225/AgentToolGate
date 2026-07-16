import * as React from "react";
import { cva, type VariantProps } from "class-variance-authority";
import { cn } from "@/lib/utils";

const badgeVariants = cva(
  "inline-flex items-center rounded-full border px-2.5 py-0.5 text-xs font-semibold transition-colors",
  {
    variants: {
      variant: {
        default: "border-primary/25 bg-primary/[0.14] text-primary",
        secondary: "border-border/80 bg-white/[0.055] text-muted-foreground",
        success: "border-primary/25 bg-primary/[0.14] text-primary",
        pending: "border-accent/25 bg-accent/[0.14] text-accent",
        destructive: "border-destructive/30 bg-destructive/[0.14] text-destructive",
        outline: "border-border text-foreground",
      },
    },
    defaultVariants: {
      variant: "default",
    },
  },
);

export interface BadgeProps
  extends React.HTMLAttributes<HTMLDivElement>,
    VariantProps<typeof badgeVariants> {}

function Badge({ className, variant, ...props }: BadgeProps) {
  return <div className={cn(badgeVariants({ variant }), className)} {...props} />;
}

export { Badge, badgeVariants };
