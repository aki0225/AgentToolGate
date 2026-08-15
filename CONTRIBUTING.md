# 为 AgentToolGate 贡献

## 开发环境

- Go 1.26+
- Node.js 20.x（仓库根目录 `.node-version` 是统一版本来源）
- Python 3.10+（产品 Hook 与真实 Codex 证据脚本测试）
- PostgreSQL 16（可选，默认使用 SQLite）

## 运行测试

```powershell
cd backend
go test -count=1 -timeout 60s ./...
go vet ./...

cd ../frontend
npm ci
npm run check
npm run build

cd ..
python -m unittest discover -s scripts/real-codex-demo -p "test_*.py"
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\tests\validate-release-tag.tests.ps1
```

## 本地验收

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-local.ps1
```

## 提交规范

- Git 提交信息使用中文。
- 不提交 `.env`、`.tmp/`、测试结果、构建产物或真实令牌。

## 安全问题

报告安全漏洞前，请先阅读 `SECURITY.md`。
