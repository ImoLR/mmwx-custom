# 用户生命周期修复：本地官方验证

仅允许本机官方 v0.5.5 harness。脚本固定访问 `127.0.0.1:22889`、
Custom `127.0.0.1:22890` 和 `mmwx-test-pg`；不会连接生产。
所有 fixture 的节点 owner 都是 admin，用 inbound 凭据判断业务归属。

1. 没有同名容器时，用归档的
   `/root/mmwx-custom-artifacts/package-manager-parity/restore-harness.sh`
   重建保留 volume 的 harness；已有容器则 `docker start`，不要删除。
2. **修改前备份**：

   ```bash
   mkdir -p /root/mmwx-custom-artifacts/user-lifecycle-fixes
   chmod 700 /root/mmwx-custom-artifacts/user-lifecycle-fixes
   docker exec mmwx-test-pg pg_dump -U mmwx -d mmwx -Fc -f /tmp/user-lifecycle-before.dump
   docker cp mmwx-test-pg:/tmp/user-lifecycle-before.dump /root/mmwx-custom-artifacts/user-lifecycle-fixes/harness-before.dump
   chmod 600 /root/mmwx-custom-artifacts/user-lifecycle-fixes/harness-before.dump
   ```

3. 将该目录 `fixture-config.json` 初始化为 `{"inbounds":[],"outbounds":[]}`。
   在官方容器网络命名空间启动 fake Agent（记录 PID，完成后终止）：

   ```bash
   AUDIT_OFFICIAL_LOCAL=1 nsenter -t "$(docker inspect -f '{{.State.Pid}}' mmwx-test-official)" -n \
     node tests/audit/user-manager/verify-lifecycle.mjs --agent
   ```

4. API 探测：

   ```bash
   AUDIT_OFFICIAL_LOCAL=1 NODE_OPTIONS=--max-old-space-size=768 \
     node tests/audit/user-manager/verify-lifecycle.mjs --probe
   ```

   结果写 `api-probe.json`，包含 remote remove 对 runtime/node/package 的影响、
   node REST 删除、package REST PUT 与页面 op 的对照。重新运行应先恢复 dump，
   避免 fixture 用户、凭据与节点交叉引用污染结果。

5. 在干净 dump 恢复后启动本分支已构建的 Custom（所有监听仅 loopback，数据库
   `127.0.0.1:55432/mmwx`，官方 target 为 `http://127.0.0.1:22889`），执行：

   ```bash
   AUDIT_OFFICIAL_LOCAL=1 NODE_OPTIONS=--max-old-space-size=768 \
     node tests/audit/user-manager/verify-lifecycle.mjs --e2e
   ```

   `e2e-results.json` 保存预览与实际结果。四项断言：仅本人加管理员共同凭据的节点
   随用户/套餐删除；混有另一业务用户节点的套餐保留；其它用户套餐引用的本人节点
   从列表、5 种节点覆写和 Custom 流量组成员中移除；共享认证的禁用被拒绝且配置不变。
   Fixture 通过官方创建用户/套餐，并 SQL seed admin-owned 节点、凭据、active assignment、
   snapshot 和 Custom 流量组；实际被测的生命周期动作全部调用 Custom API。

6. 收尾先停 Custom、fake Agent 和官方，再恢复 public：

   ```bash
   docker stop mmwx-test-official
   docker exec mmwx-test-pg pg_restore -U mmwx -d mmwx --clean --if-exists --no-owner -n public /tmp/user-lifecycle-before.dump
   docker stop mmwx-test-pg
   ```

   保留容器、volume、network 和原 license。核对恢复前后用户/套餐/节点计数、
   public 表与触发器。dump 是本地敏感材料，不提交。

fake Agent 沿用审计的 prepare/activate/commit 模拟，并精确处理 Custom 使用的
`/api/child/inbounds` remove/replace/remove-client。它证明官方实际请求和接受后的
配置结果，不等于真实 Core 网络握手或现存连接终止验证。
