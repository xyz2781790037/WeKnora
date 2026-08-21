# WeKnora Go 数据源插件模板

这个目录是最小可运行模板。复制到独立仓库后，主要修改三处：

1. 在 `plugin.yaml` 中填写唯一插件 ID、镜像地址、配置 Schema 和权限。
2. 在 `main.go` 中实现数据源的资源发现和同步逻辑。
3. 将源文件通过 `SyncEmitter.EmitDocument` 交给 WeKnora，不在插件中解析、分块或索引。

从 WeKnora 仓库根目录构建模板镜像：

```bash
docker build -f sdk/plugin/go/template/Dockerfile -t weknora-example-source-plugin:dev .
```

本地运行：

```bash
go run ./sdk/plugin/go/template
```

默认监听 `:9000`，可以使用 `WEKNORA_PLUGIN_LISTEN` 修改地址。

数据源实现需要提供：

- `ValidateConfig`：检查连接参数。
- `HealthCheck`：报告插件健康状态。
- `ListResources`：列出可同步资源。
- `ResolveResourceAncestors`：恢复层级选择；平铺数据源可返回空。
- `Sync`：发送新增、修改、删除事件和可恢复游标。

插件进程不能访问 WeKnora 的 PostgreSQL、Redis 或内部文件目录。
