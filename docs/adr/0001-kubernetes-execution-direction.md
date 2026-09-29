# Kubernetes 执行模型取代固定 Node 模型

状态：已接受（2026-09-27）

SwarmJet 以企业私有部署为目标，Kubernetes 接入将重新设计任务执行归属、工作区、Runner 恢复与资源准入。当前 Docker/远程 Node 模型按主机固定任务，不能靠实现同一 `execution.Node` 接口自然获得 Pod 重调度与恢复语义。新模型按[接入准备](../kubernetes-migration.md)演进，不承诺长期维护两套完整执行架构；现有任务与数据的原地迁移不属于这次接入范围。

采用 Kubernetes 后可以移除固定 Node 语义，但新模型仍必须正确处理 Workflow 历史、Pod 重试与升级兼容。代码升级不等于生产部署或数据迁移；已有任务应按独立迁移方案处理。
