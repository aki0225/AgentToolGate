# AgentToolGate Agent 安全评估

该目录保存公开、可重复生成的评估契约和用例。评估工具位于
`tools/atg-eval/`，生成物默认写入 `.tmp/evaluation/<run-id>/`，不会提交到仓库。

## 当前阶段

阶段 1 已建立：

- Case / Result v1 数据契约。
- 严格 JSONL loader。
- disposable sandbox containment。
- 只绑定 loopback 的 mock server。
- 统一文本、JSON 和 HTTP header 脱敏器。

危险动作、良性动作、治理不变量、Runner 和报告生成会在后续阶段加入。本阶段不会为了
得到更好结果修改生产 Guard、Hook Adapter 或 Policy。

## 本地验证

```powershell
go -C tools/atg-eval test ./...
go -C tools/atg-eval vet ./...
```

校验单个 JSONL：

```powershell
go -C tools/atg-eval run . validate `
  --input ..\..\evaluation\suites\dangerous-actions-v1.jsonl
```

`evaluation/suites/` 会在核心用例阶段创建。

## 安全边界

- 用例只描述受限 `operation`，Runner 不执行用例提供的任意 shell 字符串。
- 用例 target 使用 `<sandbox>` 占位符，不接受真实绝对路径。
- 网络评估只允许显式 loopback IP 和端口。
- sandbox 清理必须同时通过路径 containment、随机 nonce 标记和根目录复核。
- 脱敏失败时返回错误，不把原始 JSON 作为降级结果。
- 评估不是 OS sandbox、EDR、DLP 或完整红队平台。
