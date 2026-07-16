# 为 AgentToolGate 贡献

## 开发环境

- Go 1.21+
- Node.js 18+
- PostgreSQL 15+（可选，默认使用 SQLite）

## 运行测试

```powershell
cd backend
go test ./...

cd ../frontend
npm ci
npm run check
```

## 本地验收

```powershell
pwsh -NoProfile -ExecutionPolicy Bypass -File .\scripts\verify-local.ps1
```

## 提交规范

- Git 提交信息使用中文。
- 不提交 `.env`、`.trellis/workspace/` 或真实令牌。

## 安全问题

报告安全漏洞前，请先阅读 `SECURITY.md`。
