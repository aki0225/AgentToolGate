# AI 客户端 smoke prompts

按顺序把这些 prompt 发给 Claude Code 或 Codex，用来验证 AgentToolGate MCP Inbound 是否可用。

## 1. 工具发现

```text
请列出 AgentToolGate MCP server 暴露的所有工具，只输出工具名和一句话用途。
```

预期：能看到 `mock.echo`，以及当前 workspace 中启用的其它工具。

## 2. mock.echo 成功调用

```text
请调用 AgentToolGate 的 mock.echo 工具，参数为 {"message":"hello from ai client"}，然后告诉我返回内容和 call id。
```

预期：返回成功内容；AgentToolGate Audit Logs 出现 `mock.echo`、`success`、`policyDecision=allow`。

## 3. approval_required 语义

```text
请尝试调用一个需要审批的写操作。如果返回 approval_required，请不要把它当成失败；请告诉我 approval id，并提示我去 AgentToolGate UI 审批。
```

预期：高风险写操作在审批前不触达上游；UI 的 Approvals 页面出现 pending 审批。

## 4. 审批后复查

```text
我已经在 AgentToolGate UI 处理审批。请重试刚才的操作，或者指导我到 Audit Logs 查看最终状态。
```

预期：审批通过后再次调用或 Audit Logs 能看到 `approved` / `success`；拒绝时看到 `rejected`，且上游未执行。

## 5. 安全检查

```text
请确认你没有看到任何 GitHub token、数据库 DSN 密码、HTTP Authorization header 或 MCP 上游 Secret 明文。
```

预期：AI client 只能看到工具结果和脱敏审计信息，不应看到 Secret 解析值。
