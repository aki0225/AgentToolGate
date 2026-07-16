import { FormEvent, useEffect, useMemo, useState } from "react";
import { Link, useParams } from "react-router-dom";
import { Clock, Code2, Database, Globe, History, Play, ShieldCheck, Wrench } from "lucide-react";
import { createToolCall, getDatabaseSchema, getTool, listToolCalls } from "../api/client";
import { useAuth } from "../auth/AuthContext";
import { canExecuteTools } from "../auth/permissions";
import { Badge } from "../components/ui/badge";
import { Button } from "../components/ui/button";
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from "../components/ui/card";
import {
  Dialog,
  DialogContent,
  DialogDescription,
  DialogHeader,
  DialogTitle,
  DialogTrigger,
} from "../components/ui/dialog";
import { Input } from "../components/ui/input";
import { Label } from "../components/ui/label";
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "../components/ui/select";
import { Table, TableBody, TableCell, TableHead, TableHeader, TableRow } from "../components/ui/table";
import { Tabs, TabsContent, TabsList, TabsTrigger } from "../components/ui/tabs";
import { JsonBlock } from "../components/JsonBlock";
import { useI18n } from "../i18n";
import type { DatabaseSchemaResponse, Tool, ToolCall, ToolCallResult } from "../types";

const textareaClassName =
  "min-h-28 w-full rounded-[14px] border border-input bg-background/55 px-3 py-2 text-sm text-foreground ring-offset-background transition-colors placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50";

export function ToolDetailPage() {
  const { toolId } = useParams();
  const auth = useAuth();
  const { t } = useI18n();
  const workspaceOrgId = auth.currentWorkspace?.zitadelOrganizationId ?? auth.selectedWorkspaceOrgId ?? null;
  const token = auth.oidcUser?.id_token ?? null;
  const [tool, setTool] = useState<Tool | null>(null);
  const [calls, setCalls] = useState<ToolCall[]>([]);
  const [payload, setPayload] = useState(`{"message":"hello from AgentToolGate"}`);
  const [datasource, setDatasource] = useState("local_postgres");
  const [sql, setSql] = useState("SELECT 1 AS demo");
  const [githubOwner, setGithubOwner] = useState("acme");
  const [githubRepo, setGithubRepo] = useState("demo");
  const [githubPullNumber, setGithubPullNumber] = useState("1");
  const [githubIssueTitle, setGithubIssueTitle] = useState("Demo issue from AgentToolGate");
  const [githubIssueBody, setGithubIssueBody] = useState("Created by the governed GitHub Adapter demo.");
  const [httpMethod, setHttpMethod] = useState("GET");
  const [httpUrl, setHttpUrl] = useState("http://localhost:18080/v1/status");
  const [httpHeaders, setHttpHeaders] = useState(`{\n  "X-Demo": "hello"\n}`);
  const [httpHeaderSecretRefs, setHttpHeaderSecretRefs] = useState("");
  const [httpBody, setHttpBody] = useState("");
  const [refreshNonce, setRefreshNonce] = useState(0);
  const [result, setResult] = useState<ToolCallResult | null>(null);
  const [error, setError] = useState<string | null>(null);
  const [databaseSchema, setDatabaseSchema] = useState<DatabaseSchemaResponse | null>(null);
  const [databaseSchemaError, setDatabaseSchemaError] = useState<string | null>(null);
  const toolKey = useMemo(() => (tool ? `${tool.namespace}.${tool.name}` : ""), [tool]);

  useEffect(() => {
    if (!toolId) {
      return;
    }
    const activeToolId = toolId;
    let cancelled = false;
    async function load() {
      const toolResult = await getTool(activeToolId, token, workspaceOrgId);
      let callItems: ToolCall[] = [];
      try {
        const callsResult = await listToolCalls(token, workspaceOrgId, {
          tool: `${toolResult.namespace}.${toolResult.name}`,
          pageSize: 200,
        });
        callItems = callsResult.items;
      } catch {
        callItems = [];
      }
      if (!cancelled) {
        setTool(toolResult);
        setCalls(callItems);
      }
    }
    void load();
    return () => {
      cancelled = true;
    };
  }, [toolId, token, workspaceOrgId, refreshNonce]);

  const isMockTool = useMemo(() => tool?.namespace === "mock", [tool]);
  const isDatabaseQueryTool = useMemo(() => toolKey === "database.query", [toolKey]);
  const isGitHubTool = useMemo(() => tool?.namespace === "github", [tool]);
  const isHTTPTool = useMemo(() => toolKey === "http.request", [toolKey]);
  const isMCPTool = useMemo(() => tool?.namespace.startsWith("mcp_") ?? false, [tool]);
  const httpMethodRequiresApproval = useMemo(() => ["POST", "PUT", "PATCH", "DELETE"].includes(httpMethod.toUpperCase()), [httpMethod]);
  const canExecute = canExecuteTools(auth.me?.user.role);

  useEffect(() => {
    if (!isDatabaseQueryTool) {
      setDatabaseSchema(null);
      setDatabaseSchemaError(null);
      return;
    }
    let cancelled = false;
    async function loadSchema() {
      setDatabaseSchemaError(null);
      try {
        const result = await getDatabaseSchema(datasource || "local_postgres", token, workspaceOrgId);
        if (!cancelled) {
          setDatabaseSchema(result);
        }
      } catch (err) {
        if (!cancelled) {
          setDatabaseSchema(null);
          setDatabaseSchemaError(err instanceof Error ? err.message : t("toolDetail.schema.loadError"));
        }
      }
    }
    void loadSchema();
    return () => {
      cancelled = true;
    };
  }, [isDatabaseQueryTool, datasource, token, workspaceOrgId, t]);

  useEffect(() => {
    const firstTable = databaseSchema?.tables[0];
    if (isDatabaseQueryTool && firstTable && sql === "SELECT 1 AS demo") {
      setSql(`SELECT * FROM ${firstTable.schema}.${firstTable.table} LIMIT 10`);
    }
  }, [databaseSchema, isDatabaseQueryTool, sql]);

  async function handleRun(event: FormEvent<HTMLFormElement>) {
    event.preventDefault();
    setError(null);
    setResult(null);
    if (!canExecute) {
      setError(t("common.permissionDenied"));
      return;
    }
    try {
      if (!toolId || !tool) {
        throw new Error(t("toolDetail.notAvailable"));
      }
      let parsed: unknown;
      if (isDatabaseQueryTool) {
        parsed = { datasource, sql };
      } else if (isGitHubTool && toolKey === "github.list_repos") {
        parsed = {};
      } else if (isGitHubTool && toolKey === "github.get_pull_request") {
        parsed = { owner: githubOwner, repo: githubRepo, pullNumber: Number(githubPullNumber) };
      } else if (isGitHubTool && toolKey === "github.create_issue") {
        parsed = { owner: githubOwner, repo: githubRepo, title: githubIssueTitle, body: githubIssueBody };
      } else if (isHTTPTool) {
        const headers = httpHeaders.trim() === "" ? {} : (JSON.parse(httpHeaders) as unknown);
        const headerSecretRefs = httpHeaderSecretRefs.trim() === "" ? {} : (JSON.parse(httpHeaderSecretRefs) as unknown);
        const httpArguments: Record<string, unknown> = {
          method: httpMethod,
          url: httpUrl,
          headers,
          headerSecretRefs,
        };
        if (httpBody.trim() !== "") {
          httpArguments.body = JSON.parse(httpBody) as unknown;
        }
        parsed = httpArguments;
      } else if (isMockTool || isMCPTool) {
        parsed = JSON.parse(payload) as unknown;
      } else {
        throw new Error(t("toolDetail.unsupported"));
      }
      const response = await createToolCall({ tool: toolKey, arguments: parsed }, token, workspaceOrgId);
      setResult(response);
    } catch (err) {
      setError(err instanceof Error ? err.message : t("toolDetail.executionFailed"));
    } finally {
      setRefreshNonce((current) => current + 1);
    }
  }

  if (!tool) {
    return (
      <div className="grid gap-6">
        <Card className="p-6">
          <p className="m-0 text-sm text-muted-foreground">{t("toolDetail.loading")}</p>
        </Card>
      </div>
    );
  }

  return (
    <div className="grid gap-6">
      <header className="rounded-3xl border border-border bg-[radial-gradient(circle_at_top_right,rgba(96,165,250,0.16),transparent_34%),linear-gradient(135deg,rgba(11,19,35,0.96),rgba(7,17,31,0.9))] p-6">
        <div className="flex flex-wrap items-start justify-between gap-4">
          <div className="max-w-3xl">
            <div className="text-xs font-bold uppercase tracking-wider text-muted-foreground">{t("toolDetail.kicker")}</div>
            <h1 className="mt-3 text-3xl font-bold tracking-tight text-foreground">{tool.displayName}</h1>
            <p className="mt-3 text-sm text-muted-foreground">
              {toolKey} · {tool.operationType} · {tool.riskLevel}
            </p>
          </div>
          <div className="flex flex-wrap gap-2">
            <Badge variant={operationBadgeVariant(tool.operationType)}>{tool.operationType}</Badge>
            <Badge variant={riskBadgeVariant(tool.riskLevel)}>{tool.riskLevel}</Badge>
            <Badge variant={tool.enabled ? "success" : "destructive"}>{tool.enabled ? t("toolDetail.status.enabled") : t("toolDetail.status.disabled")}</Badge>
            {tool.requiresApproval && (
              <Badge variant="pending" className="gap-1">
                <ShieldCheck className="h-3.5 w-3.5" />
                {t("toolDetail.badge.approval")}
              </Badge>
            )}
          </div>
        </div>
      </header>

      <Tabs defaultValue="run" className="grid gap-4">
        <TabsList className="w-fit">
          <TabsTrigger value="run" className="gap-2">
            <Play className="h-4 w-4" />
            {t("toolDetail.tabs.run")}
          </TabsTrigger>
          <TabsTrigger value="schema" className="gap-2">
            <Code2 className="h-4 w-4" />
            {t("toolDetail.tabs.schema")}
          </TabsTrigger>
          <TabsTrigger value="history" className="gap-2">
            <History className="h-4 w-4" />
            {t("toolDetail.tabs.history")}
          </TabsTrigger>
        </TabsList>

        <TabsContent value="run" className="grid gap-6 xl:grid-cols-[minmax(0,1fr)_360px]">
          <Card>
            <CardHeader>
              <CardTitle>{t("toolDetail.run.title")}</CardTitle>
              <CardDescription>{t("toolDetail.run.description")}</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-4">
              {isDatabaseQueryTool ? (
                <form className="grid gap-4" onSubmit={handleRun}>
                  <div className="grid gap-2">
                    <Label htmlFor="database-datasource">{t("toolDetail.database.datasource")}</Label>
                    <Input id="database-datasource" value={datasource} onChange={(event) => setDatasource(event.target.value)} />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="database-sql">{t("toolDetail.database.sql")}</Label>
                    <textarea id="database-sql" className={textareaClassName} rows={8} value={sql} onChange={(event) => setSql(event.target.value)} />
                  </div>
                  <p className="m-0 text-sm text-muted-foreground">
                    {t("toolDetail.database.guard")}
                  </p>
                    <Button type="submit" disabled={!canExecute}>
                      {t("toolDetail.database.execute")}
                    </Button>
                </form>
              ) : isGitHubTool ? (
                <form className="grid gap-4" onSubmit={handleRun}>
                  {toolKey === "github.list_repos" ? (
                    <p className="m-0 rounded-2xl border border-border bg-white/[0.03] px-4 py-3 text-sm text-muted-foreground">
                      {t("toolDetail.github.listRepos")}
                    </p>
                  ) : (
                    <>
                      <div className="grid gap-2">
                        <Label htmlFor="github-owner">{t("toolDetail.github.owner")}</Label>
                        <Input id="github-owner" value={githubOwner} onChange={(event) => setGithubOwner(event.target.value)} />
                      </div>
                      <div className="grid gap-2">
                        <Label htmlFor="github-repo">{t("toolDetail.github.repo")}</Label>
                        <Input id="github-repo" value={githubRepo} onChange={(event) => setGithubRepo(event.target.value)} />
                      </div>
                    </>
                  )}
                  {toolKey === "github.get_pull_request" && (
                    <div className="grid gap-2">
                      <Label htmlFor="github-pull-number">{t("toolDetail.github.pullNumber")}</Label>
                      <Input
                        id="github-pull-number"
                        type="number"
                        min="1"
                        value={githubPullNumber}
                        onChange={(event) => setGithubPullNumber(event.target.value)}
                      />
                    </div>
                  )}
                  {toolKey === "github.create_issue" && (
                    <>
                      <div className="grid gap-2">
                        <Label htmlFor="github-issue-title">{t("toolDetail.github.issueTitle")}</Label>
                        <Input id="github-issue-title" value={githubIssueTitle} onChange={(event) => setGithubIssueTitle(event.target.value)} />
                      </div>
                      <div className="grid gap-2">
                        <Label htmlFor="github-issue-body">{t("toolDetail.github.issueBody")}</Label>
                        <textarea
                          id="github-issue-body"
                          className={textareaClassName}
                          rows={6}
                          value={githubIssueBody}
                          onChange={(event) => setGithubIssueBody(event.target.value)}
                        />
                      </div>
                      <p className="m-0 text-sm text-muted-foreground">
                        {t("toolDetail.github.writeNotice")}
                      </p>
                    </>
                  )}
                  <p className="m-0 text-sm text-muted-foreground">
                    {t("toolDetail.github.guard")}
                  </p>
                    <Button type="submit" disabled={!canExecute}>
                      {toolKey === "github.create_issue" ? t("toolDetail.action.requestApproval") : t("toolDetail.action.execute")}
                    </Button>
                </form>
              ) : isHTTPTool ? (
                <form className="grid gap-4" onSubmit={handleRun}>
                  <div className="grid gap-2">
                    <Label htmlFor="http-method">{t("toolDetail.http.method")}</Label>
                    <Select value={httpMethod} onValueChange={setHttpMethod}>
                      <SelectTrigger id="http-method">
                        <SelectValue />
                      </SelectTrigger>
                      <SelectContent>
                        {["GET", "HEAD", "OPTIONS", "POST", "PUT", "PATCH", "DELETE"].map((method) => (
                          <SelectItem key={method} value={method}>
                            {method}
                          </SelectItem>
                        ))}
                      </SelectContent>
                    </Select>
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="http-url">{t("toolDetail.http.url")}</Label>
                    <Input id="http-url" value={httpUrl} onChange={(event) => setHttpUrl(event.target.value)} />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="http-headers">{t("toolDetail.http.headers")}</Label>
                    <textarea id="http-headers" className={textareaClassName} rows={5} value={httpHeaders} onChange={(event) => setHttpHeaders(event.target.value)} />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="http-header-secret-refs">{t("toolDetail.http.headerSecretRefs")}</Label>
                    <textarea
                      id="http-header-secret-refs"
                      className={textareaClassName}
                      rows={5}
                      value={httpHeaderSecretRefs}
                      onChange={(event) => setHttpHeaderSecretRefs(event.target.value)}
                      placeholder='{"Authorization":"internal_api_token"}'
                    />
                  </div>
                  <div className="grid gap-2">
                    <Label htmlFor="http-body">{t("toolDetail.http.body")}</Label>
                    <textarea
                      id="http-body"
                      className={textareaClassName}
                      rows={6}
                      value={httpBody}
                      onChange={(event) => setHttpBody(event.target.value)}
                      placeholder='{"message":"hello"}'
                    />
                  </div>
                  <p className="m-0 text-sm text-muted-foreground">
                    {t("toolDetail.http.guard")}
                  </p>
                  {httpMethodRequiresApproval && (
                    <p className="m-0 rounded-2xl border border-accent/20 bg-accent/10 px-4 py-3 text-sm text-accent">
                      {t("toolDetail.http.writeNotice")}
                    </p>
                  )}
                    <Button type="submit" disabled={!canExecute}>
                      {httpMethodRequiresApproval ? t("toolDetail.action.requestApproval") : t("toolDetail.action.execute")}
                    </Button>
                </form>
              ) : isMCPTool ? (
                <form className="grid gap-4" onSubmit={handleRun}>
                  <div className="grid gap-2">
                    <Label htmlFor="mcp-arguments">{t("toolDetail.mcp.arguments")}</Label>
                    <textarea id="mcp-arguments" className={textareaClassName} rows={8} value={payload} onChange={(event) => setPayload(event.target.value)} />
                  </div>
                  <p className="m-0 text-sm text-muted-foreground">
                    {t("toolDetail.mcp.guard")}
                  </p>
                  {tool.requiresApproval && (
                    <p className="m-0 rounded-2xl border border-accent/20 bg-accent/10 px-4 py-3 text-sm text-accent">
                      {t("toolDetail.mcp.writeNotice")}
                    </p>
                  )}
                    <Button type="submit" disabled={!canExecute}>
                      {tool.requiresApproval ? t("toolDetail.action.requestApproval") : t("toolDetail.action.execute")}
                    </Button>
                </form>
              ) : isMockTool ? (
                <form className="grid gap-4" onSubmit={handleRun}>
                  <div className="grid gap-2">
                    <Label htmlFor="mock-arguments">{t("toolDetail.mock.arguments")}</Label>
                    <textarea id="mock-arguments" className={textareaClassName} rows={8} value={payload} onChange={(event) => setPayload(event.target.value)} />
                  </div>
                    <Button type="submit" disabled={!canExecute}>
                      {t("toolDetail.action.execute")}
                    </Button>
                </form>
              ) : (
                <p className="m-0 text-sm text-muted-foreground">{t("toolDetail.unsupported")}</p>
              )}

              {error && <p className="m-0 rounded-2xl border border-destructive/25 bg-destructive/10 px-4 py-3 text-sm text-destructive">{error}</p>}
              {result && (
                <div className="grid gap-3 rounded-2xl border border-border bg-white/[0.03] p-4">
                  <div className="flex flex-wrap items-center justify-between gap-3">
                    <div>
                      <div className="text-sm font-bold text-foreground">{t("toolDetail.response.title")}</div>
                      <p className="m-0 mt-1 text-xs text-muted-foreground">{t("toolDetail.response.description")}</p>
                    </div>
                    <div className="flex flex-wrap gap-2">
                      <Badge variant={statusBadgeVariant(result.status)}>{result.status}</Badge>
                      <Dialog>
                        <DialogTrigger asChild>
                          <Button type="button" variant="outline" size="sm">
                            {t("toolDetail.response.viewJson")}
                          </Button>
                        </DialogTrigger>
                        <DialogContent className="max-w-3xl">
                          <DialogHeader>
                            <DialogTitle>{t("toolDetail.response.dialogTitle")}</DialogTitle>
                            <DialogDescription>{t("toolDetail.response.dialogDescription")}</DialogDescription>
                          </DialogHeader>
                          <JsonBlock value={result} />
                        </DialogContent>
                      </Dialog>
                    </div>
                  </div>
                  <JsonBlock value={result} />
                  {result.status === "approval_required" && (
                    <p className="m-0 text-sm text-muted-foreground">
                      {t("toolDetail.response.approvalRequiredPrefix")}{" "}
                      <Link to="/approvals" className="text-accent transition-colors hover:text-primary">
                        {t("toolDetail.response.approvalsLink")}
                      </Link>{" "}
                      {t("toolDetail.response.approvalRequiredSuffix")}
                    </p>
                  )}
                </div>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("toolDetail.metadata.title")}</CardTitle>
              <CardDescription>{t("toolDetail.metadata.description")}</CardDescription>
            </CardHeader>
            <CardContent className="grid gap-3">
              <MetadataRow label={t("toolDetail.metadata.key")} value={toolKey} />
              <MetadataRow label={t("toolDetail.metadata.enabled")} value={tool.enabled ? t("toolDetail.metadata.yes") : t("toolDetail.metadata.no")} />
              <MetadataRow label={t("toolDetail.metadata.requiresApproval")} value={tool.requiresApproval ? t("toolDetail.metadata.yes") : t("toolDetail.metadata.no")} />
              <MetadataRow label={t("toolDetail.metadata.created")} value={new Date(tool.createdAt).toLocaleString()} />
              <MetadataRow label={t("toolDetail.metadata.updated")} value={new Date(tool.updatedAt).toLocaleString()} />
            </CardContent>
          </Card>
        </TabsContent>

        <TabsContent value="schema" className="grid gap-6 xl:grid-cols-2">
          <Card>
            <CardHeader>
              <CardTitle>{t("toolDetail.schema.inputTitle")}</CardTitle>
              <CardDescription>{t("toolDetail.schema.inputDescription")}</CardDescription>
            </CardHeader>
            <CardContent>
              <JsonBlock value={tool.inputSchemaJson} />
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle>{t("toolDetail.schema.outputTitle")}</CardTitle>
              <CardDescription>{t("toolDetail.schema.outputDescription")}</CardDescription>
            </CardHeader>
            <CardContent>
              <JsonBlock value={tool.outputSchemaJson} />
            </CardContent>
          </Card>

          {isDatabaseQueryTool && (
            <Card className="xl:col-span-2">
              <CardHeader>
                <div className="flex flex-wrap items-start justify-between gap-4">
                  <div>
                    <CardTitle>{t("toolDetail.schema.databaseTitle")}</CardTitle>
                    <CardDescription>{t("toolDetail.schema.databaseDescription")}</CardDescription>
                  </div>
                  <Badge variant="secondary" className="gap-1">
                    <Database className="h-3.5 w-3.5" />
                    {datasource}
                  </Badge>
                </div>
              </CardHeader>
              <CardContent className="grid gap-4">
                {databaseSchemaError && (
                  <p className="m-0 rounded-2xl border border-destructive/25 bg-destructive/10 px-4 py-3 text-sm text-destructive">
                    {databaseSchemaError}
                  </p>
                )}
                {databaseSchema?.message && <p className="m-0 text-sm text-muted-foreground">{databaseSchema.message}</p>}
                {!databaseSchema && !databaseSchemaError ? (
                  <p className="m-0 text-sm text-muted-foreground">{t("toolDetail.schema.loading")}</p>
                ) : databaseSchema && databaseSchema.tables.length > 0 ? (
                  <div className="grid gap-3">
                    {databaseSchema.tables.map((table) => (
                      <article key={`${table.schema}.${table.table}`} className="rounded-2xl border border-border bg-white/[0.03] p-4">
                        <div className="flex flex-wrap items-center justify-between gap-3">
                          <strong className="text-sm text-foreground">
                            {table.schema}.{table.table}
                          </strong>
                          <Badge variant="secondary">{t("toolDetail.schema.columns", { count: table.columns.length })}</Badge>
                        </div>
                        <div className="mt-4 flex flex-wrap gap-2">
                          {table.columns.map((column) => (
                            <Badge key={column.name} variant={column.masked ? "destructive" : "secondary"} className="gap-2">
                              <span>{column.name}</span>
                              <span className="text-[10px] uppercase tracking-wider opacity-70">{column.dataType}</span>
                              {column.masked ? <span className="text-[10px] uppercase tracking-wider">{t("toolDetail.schema.masked")}</span> : null}
                            </Badge>
                          ))}
                        </div>
                      </article>
                    ))}
                  </div>
                ) : (
                  !databaseSchemaError && (
                    <p className="m-0 text-sm text-muted-foreground">{t("toolDetail.schema.empty")}</p>
                  )
                )}
              </CardContent>
            </Card>
          )}
        </TabsContent>

        <TabsContent value="history">
          <Card>
            <CardHeader>
              <div className="flex flex-wrap items-start justify-between gap-4">
                <div>
                  <CardTitle>{t("toolDetail.history.title")}</CardTitle>
                  <CardDescription>{t("toolDetail.history.description", { count: calls.length })}</CardDescription>
                </div>
                <Badge variant="secondary" className="gap-1">
                  <Clock className="h-3.5 w-3.5" />
                  {t("toolDetail.history.badge")}
                </Badge>
              </div>
            </CardHeader>
            <CardContent>
              {calls.length === 0 ? (
                <p className="m-0 text-sm text-muted-foreground">{t("toolDetail.history.empty")}</p>
              ) : (
                <Table>
                  <TableHeader>
                    <TableRow className="bg-transparent hover:border-transparent">
                      <TableHead>{t("toolDetail.history.status")}</TableHead>
                      <TableHead>{t("toolDetail.history.policy")}</TableHead>
                      <TableHead>{t("toolDetail.history.approval")}</TableHead>
                      <TableHead>{t("toolDetail.history.duration")}</TableHead>
                      <TableHead>{t("toolDetail.history.output")}</TableHead>
                    </TableRow>
                  </TableHeader>
                  <TableBody>
                    {calls.map((call) => (
                      <TableRow key={call.id}>
                        <TableCell>
                          <Badge variant={statusBadgeVariant(call.status)}>{call.status}</Badge>
                        </TableCell>
                        <TableCell>
                          <Badge variant={policyBadgeVariant(call.policyDecision)}>{call.policyDecision}</Badge>
                        </TableCell>
                        <TableCell>
                          {call.approvalStatus ? <Badge variant={statusBadgeVariant(call.approvalStatus)}>{call.approvalStatus}</Badge> : <span className="text-muted-foreground">-</span>}
                        </TableCell>
                        <TableCell className="text-muted-foreground">{call.durationMs}ms</TableCell>
                        <TableCell>
                          <JsonBlock value={call.outputRedactedJson} className="max-h-28 p-3" />
                        </TableCell>
                      </TableRow>
                    ))}
                  </TableBody>
                </Table>
              )}
            </CardContent>
          </Card>
        </TabsContent>
      </Tabs>

      <div className="flex flex-wrap gap-2 text-sm text-muted-foreground">
        <ToolFamilyIcon namespace={tool.namespace} />
        <span>{tool.description}</span>
      </div>
    </div>
  );
}

function MetadataRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex items-center justify-between gap-4 rounded-2xl border border-border bg-white/[0.03] px-4 py-3">
      <dt className="text-xs uppercase tracking-wider text-muted-foreground">{label}</dt>
      <dd className="m-0 truncate text-right text-sm font-bold text-foreground">{value}</dd>
    </div>
  );
}

function ToolFamilyIcon({ namespace }: { namespace: string }) {
  if (namespace === "database") {
    return <Database className="h-4 w-4 shrink-0 text-muted-foreground" />;
  }
  if (namespace === "github") {
    return <Code2 className="h-4 w-4 shrink-0 text-muted-foreground" />;
  }
  if (namespace === "http") {
    return <Globe className="h-4 w-4 shrink-0 text-muted-foreground" />;
  }
  return <Wrench className="h-4 w-4 shrink-0 text-muted-foreground" />;
}

function statusBadgeVariant(status: string): "success" | "pending" | "destructive" | "secondary" {
  switch (status.toLowerCase()) {
    case "success":
    case "approved":
    case "allow":
      return "success";
    case "approval_required":
    case "pending":
      return "pending";
    case "failed":
    case "denied":
    case "rejected":
      return "destructive";
    default:
      return "secondary";
  }
}

function policyBadgeVariant(policyDecision: string): "success" | "pending" | "destructive" | "secondary" {
  switch (policyDecision.toLowerCase()) {
    case "allow":
      return "success";
    case "require_approval":
    case "approval_required":
      return "pending";
    case "deny":
    case "denied":
      return "destructive";
    default:
      return "secondary";
  }
}

function operationBadgeVariant(operationType: string): "pending" | "destructive" | "secondary" {
  switch (operationType.toLowerCase()) {
    case "write":
    case "create":
    case "update":
    case "delete":
    case "patch":
    case "post":
      return "pending";
    default:
      return "secondary";
  }
}

function riskBadgeVariant(riskLevel: string): "pending" | "destructive" | "secondary" {
  switch (riskLevel.toLowerCase()) {
    case "high":
    case "critical":
      return "destructive";
    case "medium":
      return "pending";
    default:
      return "secondary";
  }
}
