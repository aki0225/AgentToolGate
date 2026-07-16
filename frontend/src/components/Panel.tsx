import type { ReactNode } from "react";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "./ui/card";

export function Panel({
  title,
  subtitle,
  children,
  actions,
}: {
  title?: string;
  subtitle?: string;
  children: ReactNode;
  actions?: ReactNode;
}) {
  return (
    <Card>
      {(title || subtitle || actions) && (
        <CardHeader>
          <div>
            {title && <CardTitle>{title}</CardTitle>}
            {subtitle && <CardDescription>{subtitle}</CardDescription>}
          </div>
          {actions}
        </CardHeader>
      )}
      <CardContent>{children}</CardContent>
    </Card>
  );
}
