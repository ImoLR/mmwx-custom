# 官方 v0.5.5 本地探测

这些 `.mjs` 默认不执行。依赖本机已归档的官方 v0.5.5 binary/seed/test license、Secure Channel client，以及 `/root/mmwx-custom-artifacts/user-manager-audit/official/assets/`。不适用于生产；HTTP固定127.0.0.1:22889，DB固定容器mmwx-test-pg，fake Agent只绑定其network namespace的127.0.0.1:23889。

## 一次完整复现

1. 无其他会话使用本地测试harness时，用 `/root/mmwx-custom-artifacts/package-manager-parity/restore-harness.sh` 恢复容器。该脚本使用保留的Postgres volume及已激活license；不要删除volume或license。
2. 先备份：`docker exec mmwx-test-pg pg_dump -U mmwx -d mmwx -Fc -f /tmp/audit-user-repro-before.dump`。另存host副本并chmod600；不要提交DB dump。
3. 保留seed里有Custom删除trigger。为纯官方对照，执行 `docker exec mmwx-test-pg psql -U mmwx -d mmwx -c 'ALTER TABLE public.users DISABLE TRIGGER mmwxc_delete_management_user_relations_trigger'`。如果原seed没有此trigger则跳过。
4. 从worktree根运行 `AUDIT_OFFICIAL_LOCAL=1 node tests/audit/user-manager/probe-official.mjs`，覆盖官方离线disable/enable/delete和独享套餐保留。
5. 写入空fixture文件 `{"inbounds":[],"outbounds":[]}` 到 `/root/mmwx-custom-artifacts/user-manager-audit/official/fixture-config.json`。在单独终端执行 `nsenter -t "$(docker inspect -f '{{.State.Pid}}' mmwx-test-official)" -n node tests/audit/user-manager/capture-pull.mjs`，记下这个进程PID。该进程没有宿主机公开监听端口。
6. 运行 **一次** `AUDIT_OFFICIAL_LOCAL=1 AUDIT_ASSERT_NATIVE_DISABLED=1 node tests/audit/user-manager/probe-official-repush.mjs`。它建立本地用户、节点和套餐，把server1临时改为loopback pull，模拟Custom禁用状态，并逐入口捕获真实官方出站事务。当前UA-O01应退出1，`repush-results.json`中仍保留完整矩阵。可把断言换为 `AUDIT_ASSERT_DISABLED=1`（UA-A08）或 `AUDIT_ASSERT_NEW_ACCESS=1`（S2/UA-A03）；各自在还原seed后独立运行。每次启动都把该fixture官方is_active置1及disabled_access_enforced置0，避免上一次native control污染初始状态。
7. 运行 `AUDIT_OFFICIAL_LOCAL=1 node tests/audit/user-manager/probe-official-delete-relations.mjs`，验证assignment解绑、其他套餐引用owned node、forward链关联、API token/WebAuthn原生清理差异。
8. 结束后先停止fake Agent进程与mmwx-test-official；使用 `docker exec mmwx-test-pg pg_restore -U mmwx -d mmwx --clean --if-exists --no-owner -n public /tmp/audit-user-repro-before.dump` 恢复完整public（包括原trigger启用状态、seed及sequence）。若开启过数据库SQL日志，恢复原log_statement设置。
9. 执行 `docker stop mmwx-test-official mmwx-test-pg`。如果本轮新建了容器，随后仅 `docker rm mmwx-test-official mmwx-test-pg`，**保留原PG volume、network和env目录/测试license**。

## 结果与限制

主材料在 `/root/mmwx-custom-artifacts/user-manager-audit/official/detailed-official.md`。最终 `probe-results.json`、`delete-relations-results.json`是在禁用Custom trigger后重跑；后缀mixed-custom-trigger为初轮受Custom补清理影响的样本。

fake Agent实现配置事务prepare/activate/commit的最小模拟；它捕获了官方实际发送的配置并模拟接受，不证明真实Core网络握手、限流、已有连接终止。Agent重连、定时到期和每日full reconcile没有运行时确认，不能从本测试推测其结果。

所有账号/UUID只为local fixture，结果敏感材料mode600。脚本创建、禁用、修改、删除本地测试数据，必须按以上dump恢复。普通 `go test ./...`、`npm test`不会启动此harness或执行这些变更。
