# 原生共享 MLflow 与用户帮助汇总

2026-09-12。用户明确要求程序使用全部共享 MLflow，并把普通使用说明分类汇总、移除管理员文档；随后明确授权原生令牌拥有与现有网页一致的创建/修改/删除能力，以及一枚一天专用验收 PAT，验收后撤销。

## 变更范围

- 原生 SDK Tracking URI：`https://raytrain.wellspiking.ai/api/v1/mlflow-native`，个人 PAT 显式选择 `mlflow:full`。共享实验、Run、Artifact、模型注册表使用 MLflow 原生协议和原生 ID；已有令牌不自动升级。
- 保留原生共享浏览器入口；平台训练 Job API、独立实验受限 SDK/REST、集成 grant 和受控产物接口继续兼容。
- Portal 实验中心以全部共享 MLflow 为主入口，独立实验与受限集成放入高级能力。
- 用户帮助按操作场景汇总，运维文档从普通阅读、搜索和导出中移出；历史和管理员管理数据保留。
- 不修改调度、GPU 配额、训练运行时或个人数据，不提交训练任务。

## 发布前证据

旧后端 `80dd1642a274b664e532a9c559dee16a8f93d927`，Portal dev `8113e99d8e1f0754bd830d027624a95cdfae1686`，远端实时核对一致。

原生共享网页 POST `/mlflow/api/2.0/mlflow/experiments/search` 返回 200，活动实验 5 个，证明此前网页确实是共享视图；程序入口尚无同等能力。构建机新增真实注册路由 RED 测试复现 native 路由 404 与 `mlflow:full` 未支持，日志 `/tmp/rtp-native-red.log`。

隔离原生服务器使用与生产一致的 MLflow 3.14.0 镜像，独立 SQLite 与临时文件目录；不会接触生产 PostgreSQL 或现有 Artifact。生产部署继续只更新后端镜像和独立 Portal dev。

## 最终发布与验收

待候选验证、发布和生产专用资源读写完成后填入版本、摘要、帮助条数、SDK 实际结果与令牌撤销证据。此段未填写前不得称本批已完成。
