# AGENTS.md

给在本仓库工作的 AI 编码代理(及新加入的人)的项目约定。

## 项目一句话

AgentToolGate(ATG)是跑在本地的 AI Agent 工具调用治理网关:工具调用在真正执行之前,先过 policy、审批、密钥注入和审计。

## 目录地图

- `backend/` — Go 后端(module `agenttoolgate/backend`),入口 `cmd/server`,核心逻辑在 `internal/`,数据库迁移在 `migrations/`
- `frontend/` — React + TypeScript 控制台(Vite),e2e 测试在 `frontend/e2e/`
- `configs/` — 默认策略与配置样例
- `deployments/`、`docker-compose.yml` — 本地部署与 PostgreSQL 集成环境
- `scripts/` — 发布构建脚本(`build-release.ps1` / `build-local.ps1`)
- `docs/` — 架构、威胁模型、策略与审批文档

## 验证命令(改完代码必须跑)

```powershell
# 后端
cd backend
go test ./...
go vet ./...

# 前端
cd frontend
npm run check
npm run build

# 前端 e2e(改动前端交互或审批流后跑)
npm run e2e
```

CI(`.github/workflows/ci.yml`)跑的就是上述集合,外加 PostgreSQL 集成测试。本地绿不等于 CI 绿,提交前至少保证上面的命令全过。

## 代码约定

- 注释与文档使用简体中文;注释只写代码本身表达不了的约束,不复述代码
- 错误处理不吞错:拒绝、降级、fail-closed 的分支必须有明确原因返回
- 修改功能时删除旧实现,不保留兼容性死代码

## 红线(违反即错,无需讨论)

1. **`.claude/hooks/` 与 `.codex/hooks/` 是产品本体**——它们是 ATG 的 Hook Adapter(把宿主工具调用送进网关评估),不是本仓库的开发配置。不要"顺手修复"、迁移或删除它们;改动它们等于改产品功能,需走正常评审。
2. **Secret 相关代码是密钥管理功能,不是泄漏**——`internal/` 中处理 secret 的代码、`.env.example` 中留空的敏感项、测试里的假密钥都是产品设计的一部分,不要当作安全事故"修复"。真正的红线是:任何真实密钥、token、个人信息不得进入代码、配置样例或提交历史。
3. **安全语义只许收紧,不许放松**——"后端离线时高风险操作保守拒绝""审批授权单次消费、限有效期"这类 fail-closed 行为是产品承诺;任何让"证据不足/服务不可用时默认放行"的改动都是 bug,不是优化。
4. **发布脚本改动必须跑 smoke**——改 `scripts/build-release.ps1` 后至少在一个平台完整跑一次构建含 smoke 校验,不许只改不验。
5. 文档口径与 README 的「防护范围 / 非目标 / 已知限制」三节保持一致:不新增夸大能力的表述,不把"当前没做"写成"设计上不做",反之亦然。
