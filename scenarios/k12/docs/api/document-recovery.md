# 会话文件恢复与排版修订 API

[English](document-recovery.en.md) · [K12 API 总览](../../API.md)

## 目录

- [1. 来源与恢复](#1-来源与恢复)
- [2. 查询和恢复原文件](#2-查询和恢复原文件)
- [3. 普通会话重新导出](#3-普通会话重新导出)
- [4. 原生报告重新导出](#4-原生报告重新导出)

## 1. 来源与恢复

沿用 `/api/k12` 现有应用装配入口。普通会话文件不要求学生档案，不写学生、错题或学情记录。需服务认证；远端核对服务所有者或原会话发送人，教学报告沿用 Agent/来源所有者校验。

文件准备前持久化原助手消息、完整正文、原附件和目标快照。恢复只接续文件或原投递批次，`model_invocations_added` 为 0。明确的零发送上传失败可恢复；上传/发送结果未知时只查询原回执，不盲重发。HTTP 成功不代表用户已阅读，以实际批次和逐项回执为准。

重发原件返回原冻结字节。重新导出生成独立文件，保留原结果和原件。必填请求头 `Idempotency-Key` 代表本次明确导出请求；相同 key 复用首次冻结的模板与结果，新的导出请求使用新 key。

## 2. 查询和恢复原文件

`GET /api/k12/final-replies/{message_id}?agent={agent}`

ID 为持久助手消息 ID，返回 `preparation_id`、`state`（`pending` / `delivered` / `dead`）、`failure` 和 `model_invocations_added: 0`。读取不生成、不发送。

`POST /api/k12/final-replies/{message_id}/resume?agent={agent}`

不接受替换正文、目标或模型参数。返回实际 `batch` 和 `model_invocations_added: 0`。已完成批次重放不再次生成或发送；未知结果保持未知。依赖/投递失败返回 502，保留 `batch` 和 `error`。

## 3. 普通会话重新导出

`POST /api/k12/final-replies/{message_id}/reexport?agent={agent}`

必填 `Idempotency-Key`。使用原内容、原附件和本次模板生成并投递新修订，返回实际 `batch`；相同 key 重放不重复投递。专业教学 PDF 使用下节产物入口。

## 4. 原生报告重新导出

`POST /api/k12/print-artifacts/{id}/reexport?agent={agent}[&send=true]`

必填 `Idempotency-Key`，ID 为原 PDF 打印产物 ID。教学报告从原批改终稿或文字结算读回类型化报告和原图，采用当前已安装模板；答案、日期、学科和身份仍来自原结果，不重新调用模型。普通纯文字打印产物也支持重排；没有持久图片来源的历史 Markdown 图片产物返回明确错误，不丢图生成不完整文件。

默认只生成文件，返回 `artifact`（含新 ID、下载 URL、摘要和大小）、`replayed`、`model_invocations_added: 0`。首次 201，重放 200。使用既有 `GET /api/k12/print-artifacts/{id}/content?agent={agent}` 下载。显式 `send=true` 沿现有有效绑定投递新产物并返回实际 `batch`；发送失败返回 502，保留产物与批次。

缺少必填参数/key 为 400；范围内不存在或无权访问为 404；来源、资源或状态冲突为 409。既有正文、图片、文件摘要和冻结版本校验继续适用。
