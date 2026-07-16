import * as React from "react";
import { toast } from "sonner";
import { useI18n } from "../i18n";
import { cn } from "@/lib/utils";

type JsonBlockProps = {
  value: unknown;
  className?: string;
};

const jsonTokenPattern = /("(?:\\.|[^"\\])*"(?=\s*:))|("(?:\\.|[^"\\])*")|(-?\b\d+(?:\.\d+)?(?:[eE][+\-]?\d+)?\b)|(true|false|null)/g;

export function JsonBlock({ value, className }: JsonBlockProps) {
  const { t } = useI18n();
  const text = stringifyJson(value);

  async function copyJson() {
    try {
      await navigator.clipboard.writeText(text);
      toast.success(t("jsonBlock.copySuccess"));
    } catch {
      toast.error(t("jsonBlock.copyError"));
    }
  }

  return (
    <div className="group relative">
      <button
        type="button"
        className="absolute right-2 top-2 z-10 rounded-[12px] border border-border/70 bg-card/90 px-2.5 py-1 text-xs font-bold text-muted-foreground opacity-80 shadow-[0_10px_24px_rgba(2,6,23,0.26)] backdrop-blur transition hover:text-foreground hover:opacity-100 focus:opacity-100 focus:outline-none focus:ring-2 focus:ring-ring md:opacity-0 md:group-hover:opacity-100"
        onClick={() => void copyJson()}
        aria-label={t("jsonBlock.copy")}
      >
        {t("jsonBlock.copy")}
      </button>
      <pre className={cn("max-h-[400px] overflow-auto rounded-2xl border border-border bg-background/70 p-4 font-mono text-xs leading-relaxed text-slate-200", className)}>{renderJson(text)}</pre>
    </div>
  );
}

function renderJson(text: string): React.ReactNode {
  const lines = text.split("\n");
  return lines.map((line, index) => (
    <div key={`${index}-${line}`} className="whitespace-pre">
      {highlightJsonLine(line)}
    </div>
  ));
}

function highlightJsonLine(line: string): React.ReactNode[] {
  const parts: React.ReactNode[] = [];
  let lastIndex = 0;

  for (const match of line.matchAll(jsonTokenPattern)) {
    const index = match.index ?? 0;
    if (index > lastIndex) {
      parts.push(<span key={`${index}-plain`}>{line.slice(lastIndex, index)}</span>);
    }

    const token = match[0];
    const isKey = Boolean(match[1]);
    const isString = Boolean(match[2]);
    const isNumber = Boolean(match[3]);
    const isBooleanOrNull = Boolean(match[4]);
    let className = "text-slate-200";
    if (isKey) {
      className = "text-emerald-300";
    } else if (isString) {
      className = "text-sky-300";
    } else if (isNumber) {
      className = "text-amber-300";
    } else if (isBooleanOrNull) {
      className = "text-fuchsia-300";
    }
    parts.push(
      <span key={`${index}-${token}`} className={className}>
        {token}
      </span>,
    );
    lastIndex = index + token.length;
  }

  if (lastIndex < line.length) {
    parts.push(<span key={`${line.length}-tail`}>{line.slice(lastIndex)}</span>);
  }

  return parts;
}

function stringifyJson(value: unknown): string {
  if (value === null || value === undefined || value === "") {
    return "{}";
  }
  if (typeof value === "string") {
    try {
      return JSON.stringify(JSON.parse(value) as unknown, null, 2);
    } catch {
      return value;
    }
  }
  try {
    return JSON.stringify(value, null, 2);
  } catch {
    return String(value);
  }
}
