# 用户管理功能：官方 v0.5.5 本地验收

仅用于已保留的本地官方 harness，禁止连接生产。节点创建使用官方 REST；
SQL 只准备合成归属、流量、过期状态及管理员共同凭据夹具。
原始证据保存在 `/root/mmwx-custom-artifacts/user-manager-features/`，权限 0700；
数据库备份、配置及结果文件权限 0600。未运行真实 Core，fake Agent 只证明配置事务。

1. 已有容器使用 `docker start mmwx-test-pg mmwx-test-official`。
   仅当两个容器均不存在时，使用已有的
   `/root/mmwx-custom-artifacts/package-manager-parity/restore-harness.sh`。
   保留测试许可证。PG 绑定 `127.0.0.1:55432`，官方绑定 `127.0.0.1:22889`。
2. 等待 `docker exec mmwx-test-pg pg_isready -U mmwx -d mmwx` 成功后，修改前备份：

   ```bash
   mkdir -p /root/mmwx-custom-artifacts/user-manager-features
   chmod 700 /root/mmwx-custom-artifacts/user-manager-features
   docker exec mmwx-test-pg pg_dump -U mmwx -d mmwx -Fc -f /tmp/user-manager-features-before.dump
   docker cp mmwx-test-pg:/tmp/user-manager-features-before.dump /root/mmwx-custom-artifacts/user-manager-features/harness-before.dump
   chmod 600 /root/mmwx-custom-artifacts/user-manager-features/harness-before.dump
   ```

3. 初始化 `fixture-config.json` 为 `{"inbounds":[],"outbounds":[]}`，权限 0600。
   在官方容器的网络命名空间运行 fake Agent，仅监听 loopback：

   ```bash
   AUDIT_OFFICIAL_LOCAL=1 NODE_OPTIONS=--max-old-space-size=768 \
     nsenter -t "$(docker inspect -f '{{.State.Pid}}' mmwx-test-official)" -n \
     node tests/audit/user-manager/verify-features.mjs --agent
   ```

   官方容器重启后必须重新启动此 Agent，避免继续使用旧网络命名空间。
4. `GOMAXPROCS=2 go build -p 1` 构建本分支，使用独立 state 目录启动 Custom。
   `MMWXC_API_LISTEN_ADDR=127.0.0.1:22890`，官方 target/internal target 均为
   `http://127.0.0.1:22889`，管理员数据库指向上述本地 PG。
   不使用其他 checkout 的进程、PID 文件或 state。然后执行：

   ```bash
   AUDIT_OFFICIAL_LOCAL=1 NODE_OPTIONS=--max-old-space-size=768 \
     node tests/audit/user-manager/verify-features.mjs --e2e
   ```

   脚本验证独立 assignment 增改解、跨用户拒绝、legacy 绑定/解绑、继承选择及
   省略参数默认、GiB 覆写、昵称回退、IP 动作、自定义过期续费、禁用确认与删除态
   拒绝、导入节点清空及变空套餐删除、管理员更换/实际修复后删除预览、用户删除。
   `e2e-results.json` 最后一项必须为 `all feature writes verified`。
   重跑前先恢复 dump，不能在残留 fixture 上重复执行。
5. 先停止本次 Custom、fake Agent 和官方容器，再恢复数据库：

   ```bash
   docker stop mmwx-test-official
   docker exec mmwx-test-pg pg_restore -U mmwx -d mmwx --clean --if-exists --no-owner -n public /tmp/user-manager-features-before.dump
   docker stop mmwx-test-pg
   ```

   不移除容器、volume、network 或许可证。恢复后核对 users=4、packages=1、nodes=3，
   无 `uf-` fixture，原 Custom 删除 trigger 启用。保留 dump 供审阅与复现。

Go 全套的 `MMWXC_TEST_POSTGRES_DSN` 应指向一次性空数据库，结束后删除；
部分既有测试要求空 schema，不得直接给它们 harness public。新回归自行建隔离 schema。
前端执行 `npm test`、`NODE_OPTIONS=--max-old-space-size=768 npm run build`；
重型任务串行。

本轮接口发现：assignment 的 REST `DELETE /api/admin/package-assignments` 请求体
必须使用普通 JSON，响应仍加密；加密请求体返回 400。独立官方 op 不需要因此使用。
管理员凭据更换保留 `77d5514260062000`；套餐清理复用既有 `f9bed75c75a38c5f`，
升级官方时必须重新核对。其他本轮新增业务操作采用已实测 REST。
