# AgentToolGate 评估附件

这个附件用于在 disposable 目录中复跑 AgentToolGate 的公开安全评估。它包含同平台的
`agenttoolgate`、`atg-eval`、固定 suites、JSON Schema、`BUILD-METADATA.json` 和许可
证，以及治理不变量评估所需的 Claude / Codex 产品 Hook，不包含预先生成的通过结果。
构建元数据记录版本、源码 commit、平台和两个二进制文件名。

## 快速校验

Windows：

```powershell
.\atg-eval.exe validate --input .\evaluation\suites\pr-quick-v1.jsonl
```

Linux：

```bash
./atg-eval validate --input ./evaluation/suites/pr-quick-v1.jsonl
```

成功时应返回 `schemaVersion=v1` 和 `caseCount=20`。

要复跑包含治理不变量的 quick suite：

```powershell
.\atg-eval.exe run `
  --input .\evaluation\suites\pr-quick-v1.jsonl `
  --atg .\agenttoolgate.exe `
  --run-id release-quick `
  --output .\proof-packs\release-quick `
  --sandbox-base .\.tmp\evaluation
```

Linux 使用无扩展名二进制和 `/` 路径分隔符。运行目录中必须保留附件自带的
`.codex/hooks/` 与 `.claude/hooks/`，否则治理不变量会保守失败。

## 运行完整评估

先创建独立输出与 sandbox 父目录。下面以危险动作 suite 为例：

```powershell
.\atg-eval.exe run `
  --input .\evaluation\suites\dangerous-actions-v1.jsonl `
  --atg .\agenttoolgate.exe `
  --run-id release-dangerous `
  --output .\proof-packs\release-dangerous `
  --sandbox-base .\.tmp\evaluation
```

Linux 使用同名无扩展名二进制，并把路径分隔符改为 `/`。另外两套完整 suite 位于
`evaluation/suites/benign-development-v1.jsonl` 和
`evaluation/suites/governance-invariants-v1.jsonl`。

评估器只执行代码中登记的受限 operation，不执行 suite 提供的任意 shell 字符串。它
仍然不是 OS sandbox；请只在 disposable 目录中运行，并在发布前核对生成的 manifest、
evidence 和 SHA256。
