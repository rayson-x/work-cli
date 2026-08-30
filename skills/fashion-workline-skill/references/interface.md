# Workline CLI Interface

这是 Agent 唯一可用的 Workline 数据操作面。CLI 承担飞书 Base 建表、字段、分页、业务 ID、关联、幂等与恢复；Agent 承担身份、Event、Style 和关系的语义判断。不要直接操作飞书 CRUD，也不要新增第四个入口。

## 固定入口与初始化

```text
lark-cli workline +query --json '<request>' [--base-token <token>] [--as user]
lark-cli workline +apply --json '<request>' [--base-token <token>] [--as user]
lark-cli workline +style-events --style-id <id> [--base-token <token>] [--as user]
```

接口版本固定为 `workline.v1`。唯一显式初始化是 `lark-cli auth login`。第一次 `+apply` 未传 Base token 时，CLI 会创建 Workline Base、补齐缺失表和字段，并将 token 保存到当前 profile；以后命令自动复用。已有 Base 也会在 `+apply` 时增量补齐缺失结构，不要求人工先建表。优先使用 user identity；bot 创建 Base 时 CLI 会尝试把当前登录用户加入该 Base。

`+query` 和 `+style-events` 只读，不会静默创建 Base；尚未运行过 `+apply` 时应先完成一次实际写入。首次成功 `+apply` 会在 `data.result.base_token` 返回共享 Base token。多台电脑协作时应由企业配置分发同一个 token（profile、`WORKLINE_BASE_TOKEN` 或 `--base-token`），不能让每台电脑各自无 token 启动并创建彼此隔离的 Base。

## Query 的精确形状

```json
{
  "interface_version": "workline.v1",
  "operation_id": "q-unique-id",
  "query": "evidence",
  "filters": {"source_key": "stable-source-key"}
}
```

所有筛选条件必须放在 `filters`，不要把业务筛选放在顶层。

| query | 常用 filters | 返回目的 |
|---|---|---|
| `evidence` | `source_key`、来源字段、`event_id`、`from/to` | 查原始证据及它已支持的全部 Event |
| `event` | `event_id`、Event 字段、`style_id`、`evidence_id`、`actor_identity_id`、`from/to` | 跨证据查重并取 Style、Evidence、来源身份、关系 |
| `style` | `style_id`、`name`、`style_code`、`aliases`、`identifier_value/identifier_kind/issuer_or_scope`、`event_id` | 查候选/正式款式、结构化编号及其 Event、Evidence |
| `person` | `person_id/name`、`identity_id/wechat_id/source_identity_key/display_name`、`identity_kind/identity_scope`、`function/scope_type/scope_key` | 查询 SourceIdentity、可选 Person 和两者的 RoleClaim；匿名来源身份不冒充微信 ID，也不会因 Person unresolved 被过滤掉 |
| `context` | Evidence 来源字段、`from/to` | 从一组证据取得相连 Event、Style 和上下文表 |
| `operation` | `operation_id`、`state` | 检查写入、合并或拆分的恢复状态 |

`from/to` 对 Evidence 使用 `source_time`，对 Event 使用 `occurred_at`。同一 Evidence 已有一个 Event 不代表语义已穷尽；必须查看返回的全部关联 Event。需要从 Event 或 Style 出发取上下文时，分别使用 `query=event` 或 `query=style`，不要把 `event_id/style_id` 交给 `query=context`。

## Apply 的精确形状

```json
{
  "interface_version": "workline.v1",
  "operation_id": "op-unique-id",
  "actions": [
    {"type": "<allowed-action>", "payload": {}}
  ]
}
```

同一 `operation_id` 只能重试完全相同的逻辑请求。CLI 会记录每个成功 action；中断后用相同请求恢复。需要改变任何判断时使用新的 operation ID。

| action | payload（业务 ID，不是飞书 record_id） |
|---|---|
| `identity.upsert` | `wechat_id` 与 `source_identity_key` 至少提供一个；可选 `identity_id/display_name/identity_kind/identity_scope/mapping_status/mapping_basis/person`。真实微信账号写 `wechat_id` 并使用 `identity_kind=wechat_id`；转发匿名作者写完整且命名空间化的 `source_identity_key`、`identity_kind=forward_hash` 和限定来源语境的 `identity_scope`，此时 `wechat_id` 留空。`person` 可为已有 `person_id`，也可为含 `person_id/name/organization/functions/identity_status/notes` 的对象；没有可靠映射时不要强建 Person。CLI 固定 `platform=wechat`。 |
| `role_claim.upsert` | `person` 与 `source_identity` 必须且只能提供一个；必填 `function/scope_type`；可选 `scope_key/organization/valid_from/valid_to/supporting_evidence/status/role_claim_id`。重复声明会合并支持证据。未映射 Person 的转发身份将角色挂在 SourceIdentity，不创建虚假 Person。 |
| `evidence.upsert` | 必填 `wechat_owner_id/conversation_id/message_id/content_type`；可选 `source_key/forward_path/speaker_identity/source_time/reply_to_evidence_id/conversation_type/excerpt/image/raw_locator/content_hash/evidence_id`。 |
| `event.create` | 必填 `summary/expression_mode` 和 `evidence_id` 或 `evidence_ids`；可选 `event_id/event_type/actors/actor_identities/occurred_at/time_basis/style_id/style_ids`。`actors` 指已解析 People，`actor_identities` 保留实际微信来源身份；Person unresolved 时后者仍必须写入。跨 action 引用时主动提供稳定 `event_id`。语义判断可以先于 Style；工程提交完成后 active Event 必须已有 Style，或在同一 operation 中由后续 `style.create(created_from_event_id=...)` 派生 candidate/confirmed Style。`style_ids` 只建立 proposed 关联；确认关系另用 `event_style.set`。 |
| `event.attach_evidence` | 必填 `event_id/evidence_id`（也接受 `event/evidence`）；可选 `support_type/interpretation`。`support_type` 仅为 `direct/supporting/confirming/contradicting/reported`。 |
| `event.relate` | 必填 `from_event/relation_type/to_event/relation_status`；可选 `basis/relation_id/created_operation_id`。 |
| `style.create` | `created_from_event_id` 与 `created_from_evidence_id` 至少提供一个，且至少有 `name/style_code/aliases/representative_images` 之一；可选 `style_id/style_status/attributes/link_status`。`style_status` 仅为 `candidate/confirmed`。只由 Evidence 建立的候选款不创建 EventStyleLink；candidate 与 Event 默认建立 proposed 关联，旧式 Event-only 请求仍兼容为 confirmed Style 与 confirmed 关联。不得仅因 merge 把 candidate 静默升级为 confirmed。 |
| `style_identifier.upsert` | 必填 `style/identifier_value/identifier_kind/issuer_or_scope`；可选 `identifier_id/normalized_value/supporting_evidence/status`。唯一性按来源范围、编号种类与规范值判断；同一 Style 可有多个编号。OCR 存疑使用候选种类/状态，不能直接确认 Style。 |
| `event_style.set` | 必填 `event_id/style_id/link_status`（也接受 `event/style/status`）；可选 `basis/link_id/created_operation_id/revision`。状态仅为 `proposed/confirmed/rejected/removed`。 |
| `event.merge` | 必填 `winner` 和非空 `losers` 数组。CLI 迁移 Evidence、Style 和 EventRelations，再保留旧 Event 的 canonical 指向。 |
| `event.split` | 必填旧 `event_id` 和非空 `events` 数组；每个新 Event 必须有 `summary/expression_mode` 及 `evidence_id(s)`，可有 `event_id/event_type/actors/occurred_at/time_basis/style_ids/relations`。Style 关联沿用 proposed，Evidence 可共享。 |
| `style.merge` | 必填 `winner` 和非空 `losers` 数组。CLI 迁移 Event 关联并保留原确认程度，合并别名/代表图，保留旧 Style canonical 指向。 |

时间使用 ISO 8601 字符串。图片字段接受已有 `file_token`，或相对 CLI 当前工作目录的路径/`{"path":"..."}`；不要提交绝对路径。`content_type` 支持 `text/image/video`。视频原消息通过 `raw_locator` 保存来源媒体坐标、内容 hash、时长和派生音画证据；`image` 可上传带时间标注的联系表或代表帧，用于飞书可视核查，但不得把派生帧伪装成原始图片消息。当前不直接上传原视频附件。

Evidence 与 Event 的关系类型必须按证据在结论中的作用选择：`direct` 是直接陈述该业务发生；`supporting` 是补充对象、原因、细节或视觉信息；`confirming` 是后续确认同一结论；`contradicting` 是与结论冲突、需要保留复核的证据；`reported` 是转述外部事项。图片不是独立的关系类型：图片直接证明结论时用 `direct`，只补充视觉细节时用 `supporting`。不要创造 `visual/context/background` 等未定义枚举；上下文但不支持 Event 的消息不建立 EventEvidenceLink。

一次 apply 可以先写 `identity/evidence`，再写引用它们的 Event/Style。跨 action 的引用必须使用请求中明确给出的业务 ID；不要用 action 数组位置代替 ID。长范围按连续片段或依赖阶段聚合 action：先批量保存已经确定来源坐标的 Evidence，再批量提交当前证据簇的 Event/Style/关系；不要为每条记录创建独立 operation，也不要等待全范围语义分析完成后才首次写入。

触发条件式身份 review 时，身份写入和复核后的 Event 写入必须使用不同的 operation ID。身份写入只提交经过查询、有稳定依据的 identity／role claim；回读后仅重新分析受身份影响的原始消息簇，再提交 Evidence／Event／Style。review 改变了任何 payload 时不得复用先前 operation ID；同一阶段因网络或中断重试时才复用原 operation ID 和完全相同请求。身份从一开始已稳定解析时不创建额外 review operation。

## 最小可执行例子

```json
{
  "interface_version": "workline.v1",
  "operation_id": "op-20260830-001",
  "actions": [
    {
      "type": "evidence.upsert",
      "payload": {
        "evidence_id": "ev-001",
        "wechat_owner_id": "wxid-owner",
        "conversation_id": "wxid-contact",
        "conversation_type": "private",
        "message_id": "msg-001",
        "content_type": "text",
        "excerpt": "已收到修改后的试片"
      }
    },
    {
      "type": "event.create",
      "payload": {
        "event_id": "evt-001",
        "summary": "收到修改后的试片",
        "event_type": "sample_received",
        "expression_mode": "fact",
        "time_basis": "message_time",
        "evidence_ids": ["ev-001"]
      }
    },
    {
      "type": "style.create",
      "payload": {
        "style_id": "style-candidate-001",
        "name": "待确认—msg-001所指试片对象",
        "style_status": "candidate",
        "created_from_event_id": "evt-001",
        "link_status": "proposed"
      }
    }
  ]
}
```

Event-first 不等于允许长期空关联。正式款号未知时，以 Event 已证明存在的具体产品对象创建可追溯的 candidate Style；连具体对象都无法确认时，不提交 `event.create`。后续确认款式时用 `event_style.set`，发现重复时用 merge，发现一个 Event 混合了多次业务发生时用 split。

## 响应、错误与回读

成功响应有 CLI 传输层和 Workline 业务层两层：

```json
{
  "ok": true,
  "identity": "user",
  "data": {
    "ok": true,
    "interface_version": "workline.v1",
    "operation_id": "op-unique-id",
    "result": {},
    "warnings": []
  }
}
```

先检查外层 `ok`，再读取 `data`。输入、授权或飞书 API 失败时使用外层结构化错误：

```json
{
  "ok": false,
  "identity": "user",
  "error": {"type": "validation", "subtype": "invalid_argument", "message": "..."}
}
```

- 未登录或权限错误：停止写入，完成登录或按 `required_scope/console_url` 授权后，用同一请求重试。
- `not_found`：重新查询稳定业务 ID；不要猜 record_id。
- `invalid_argument`：修正请求并换新 operation ID。
- operation ID 冲突：相同逻辑才复用；逻辑变化必须换 ID。
- 飞书或中断错误：先 `query=operation`，再用原请求恢复。

涉及 Style 的写入完成后统一运行，不要每写一条关系就立即回读：

```text
lark-cli workline +style-events --style-id <style-id>
```

它返回当前有效 Event、Evidence 与 EventRelations。明确 `occurred_at` 优先；缺失时才用最早 Evidence `source_time`；仍无时间的 Event 排在末尾。不存在 Timeline 表，也不维护 Timeline 序号。
