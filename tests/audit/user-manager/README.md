# 用户管理审计复现（2026-10-04）

仅审计，产品代码没有修改。完整报告和脱敏生产计数：
`/root/mmwx-custom-artifacts/user-manager-audit/audit.md`。

测试断言的是修复后应有的安全行为，当前实现下主动开启会失败。
默认 Go / npm 测试跳过这些审计案例，不连接生产，也不启动本地服务。

## 默认检查

在 worktree `/root/projects/mmwx-custom-audit-user`、分支 `audit/user-manager`：

```bash
GOMAXPROCS=2 go test -p 1 ./...
cd frontend
NODE_OPTIONS=--max-old-space-size=768 npm ci
NODE_OPTIONS=--max-old-space-size=768 npm test
```

依赖安装只需一次；勿与其他重型构建并行。

## 主动复现失败

访问生命周期使用合成凭据和 `httptest` 的官方安全通道/Agent fixture：

```bash
GOMAXPROCS=2 MMWXC_AUDIT_USER_ACCESS=1 go test -p 1 -run '^TestAuditUA_A' -v .
```

`UA-A01` 共享凭据误停；`A02` 成功启用后旧备份阻塞后续操作；
`A03` 新凭据没有备份使启用卡住；`A04` 不同用户并发覆盖恢复旧凭据；
`A05` SOCKS noauth 假成功；`A06` 单密码 Shadowsocks 无法禁用；
`A07` access 覆盖部分删除状态；`A08` 外部重推后生命周期仍显示禁用。
`A08` 单测显式模拟外部写入，实际官方触发路径另由下面的 harness 证明。

删除使用一次性本地 PostgreSQL，每项创建独立 schema，结束 `DROP SCHEMA ... CASCADE`。
DSN 强制 loopback，生产 DSN 会被拒绝；不要复用正式 schema：

```bash
# 将 MMWXC_TEST_POSTGRES_DSN 设为已启动的本地测试库 URL。
GOMAXPROCS=2 MMWXC_RUN_USER_AUDIT=1 go test -p 1 -run '^TestAuditUAD' -v .
```

`UA-D01` 删除其他用户的非活跃 assignment；`D02` 套餐级联导致重试漏掉远程凭据；
`D04` 转发链孤儿；`D05` 删除其他用户套餐引用的私有节点。

前端抽取并渲染真实 `UserCard`，没有为测试添加产品导出：

```bash
cd frontend
MMWXC_RUN_USER_AUDIT=1 NODE_OPTIONS=--max-old-space-size=768 \
  node --experimental-strip-types --test ../tests/frontend/user-manager-audit.test.ts
```

`UA-D03` 官方 inactive 用户被显示为已启用、`UA-D06` 永久套餐直接保存变有限到期、
`UA-D07` 副套餐筛选/计数漏掉多套餐用户
（均应失败）；admin 与 delete_partial
隐藏访问控制是对照案例（应通过）。

## 官方 v0.5.5 harness

`probe-official.mjs`、`probe-official-repush.mjs` 仅在显式设置
`AUDIT_OFFICIAL_LOCAL=1` 时运行。它们会修改一次性本地库；先备份、准备 fake Agent，
完成后恢复 seed/license，停止 fake Agent 和 `mmwx-test-official`、`mmwx-test-pg`。
具体准备步骤、解码资产和结果在报告链接的 `official/` 目录。

`AUDIT_ASSERT_DISABLED=1` 检查 Custom 禁用后官方重推原凭据（UA-A08）；
`AUDIT_ASSERT_NEW_ACCESS=1` 检查新增节点/套餐产生新凭据（UA-A03 的实际触发证据）；
`AUDIT_ASSERT_NATIVE_DISABLED=1` 检查官方自身禁用后的续期（UA-O01）。
纯官方删除 probe 另检查 API token/Passkey 清理漏项（UA-O02），具体开关见其说明。
这些检查预计失败。Fake Agent 证明实际发送的配置及事务提交，未运行真实 Core，
不将捕获配置误称为实际网络握手测试。

## 生产只读计数

`production-readonly.py` 是本任务特别授权下使用的清点工具，不是默认回归测试。
它在远端内存读取 `/etc/mmwx/data/database.json`，所有数据库读取使用
`BEGIN READ ONLY`，并强制连接默认只读；输出只有计数、套餐 ID、外键定义和日志计数。
凭据、用户名、原始配置和原始日志均不输出，不在远端创建文件。

```bash
ssh -o BatchMode=yes -o ConnectTimeout=10 mmwx-prod python3 - \
  < tests/audit/user-manager/production-readonly.py
```

新的任务需要独立生产访问授权。统计使用多次只读事务，存储的配置快照不等同于当前 Agent 状态。
