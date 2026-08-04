import type { SVGProps } from "react";

export type IconName =
  | "arrow"
  | "audit"
  | "check"
  | "close"
  | "download"
  | "external"
  | "gate"
  | "github"
  | "lock"
  | "menu"
  | "policy"
  | "review"
  | "secret"
  | "terminal"
  | "warning";

interface IconProps extends SVGProps<SVGSVGElement> {
  name: IconName;
}

export function Icon({ name, ...props }: IconProps) {
  return (
    <svg
      aria-hidden="true"
      fill="none"
      viewBox="0 0 24 24"
      stroke="currentColor"
      strokeWidth="1.8"
      strokeLinecap="round"
      strokeLinejoin="round"
      {...props}
    >
      {renderPath(name)}
    </svg>
  );
}

function renderPath(name: IconName) {
  switch (name) {
    case "arrow":
      return (
        <>
          <path d="M5 12h14" />
          <path d="m14 7 5 5-5 5" />
        </>
      );
    case "audit":
      return (
        <>
          <path d="M8 3h8l3 3v15H5V3h3Z" />
          <path d="M9 3v5h6V3" />
          <path d="M9 13h6" />
          <path d="M9 17h4" />
        </>
      );
    case "check":
      return <path d="m5 12 4 4L19 6" />;
    case "close":
      return (
        <>
          <path d="m6 6 12 12" />
          <path d="M18 6 6 18" />
        </>
      );
    case "download":
      return (
        <>
          <path d="M12 3v12" />
          <path d="m7 10 5 5 5-5" />
          <path d="M5 21h14" />
        </>
      );
    case "external":
      return (
        <>
          <path d="M14 5h5v5" />
          <path d="m19 5-8 8" />
          <path d="M19 13v6H5V5h6" />
        </>
      );
    case "gate":
      return (
        <>
          <path d="M4 21V7l8-4 8 4v14" />
          <path d="M8 21V9l4-2 4 2v12" />
          <path d="M12 7v14" />
        </>
      );
    case "github":
      return (
        <>
          <path d="M15 22v-4a4.8 4.8 0 0 0-1-3.5c3.3-.4 6.8-1.6 6.8-7A5.4 5.4 0 0 0 19.4 4 5 5 0 0 0 19.3.5S18.2.1 15 1.8a13.4 13.4 0 0 0-6 0C5.8.1 4.7.5 4.7.5A5 5 0 0 0 4.6 4a5.4 5.4 0 0 0-1.4 3.7c0 5.3 3.5 6.5 6.8 6.9A4.8 4.8 0 0 0 9 18v4" />
          <path d="M9 19c-3 .9-3-1.5-4.2-2" />
        </>
      );
    case "lock":
      return (
        <>
          <rect width="16" height="11" x="4" y="10" rx="2" />
          <path d="M8 10V7a4 4 0 0 1 8 0v3" />
          <path d="M12 14v3" />
        </>
      );
    case "menu":
      return (
        <>
          <path d="M4 7h16" />
          <path d="M4 12h16" />
          <path d="M4 17h16" />
        </>
      );
    case "policy":
      return (
        <>
          <path d="M12 3 4 7v5c0 5 3.4 8 8 9 4.6-1 8-4 8-9V7l-8-4Z" />
          <path d="m9 12 2 2 4-5" />
        </>
      );
    case "review":
      return (
        <>
          <path d="M8 4h8" />
          <path d="M9 2h6v4H9z" />
          <path d="M6 4H4v17h16V4h-2" />
          <path d="m8 14 2 2 5-6" />
        </>
      );
    case "secret":
      return (
        <>
          <circle cx="8" cy="15" r="4" />
          <path d="m11 12 8-8" />
          <path d="m17 4 3 3" />
          <path d="m15 8 2 2" />
        </>
      );
    case "terminal":
      return (
        <>
          <path d="m4 7 4 4-4 4" />
          <path d="M11 17h9" />
          <rect width="20" height="16" x="2" y="4" rx="2" />
        </>
      );
    case "warning":
      return (
        <>
          <path d="M12 3 2.5 20h19L12 3Z" />
          <path d="M12 9v5" />
          <path d="M12 17h.01" />
        </>
      );
  }
}
