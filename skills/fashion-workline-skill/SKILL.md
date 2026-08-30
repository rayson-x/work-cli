---
name: fashion-workline-skill
description: Extract and reconcile evidence-backed fashion workline events from specified WeChat text, images, and videos. Requires connect-wechat for local message access and uses the Workline CLI for persistence.
---

# Fashion Workline

本 Skill 把用户指定的微信文字、图片、视频、回复和转发内容整理为可核查的服装款式工作线。它只负责语义判断：哪些消息构成 Event、谁是真实发言人或行动者、Event 如何连接、可能属于哪个 Style，以及何时应保留不确定性。

当前输入范围为微信单聊、群聊、转发中的文字、图片和可读取视频；一次运行可以覆盖多个会话、多个工作线和多个微信账号。不要默认扫描全部微信，也不要把未读取的媒体、文件或沉默推断成证据。视频理解是音画联合取证，不等于只转写语音，也不要求办公电脑运行本地视觉大模型。

## 必需前置依赖：connect-wechat

读取微信前必须安装并遵循 [`connect-wechat`](https://raw.githubusercontent.com/rayson-x/wechat-cli/refs/heads/main/skills/connect-wechat/SKILL.md)。Fashion Workline 不替代、不内嵌 wechat-cli，也不能在缺少该前置能力时声称完成微信读取。它要求 Agent 与已登录的微信客户端运行在同一台 Windows／macOS 本地桌面和同一用户会话；云端、容器、远程或沙箱 Agent 不得绕过该边界读取微信。

先运行 `wechat-cli --version`，缺失或尚未初始化时严格按 `connect-wechat` 完成安装与 `init`，并以当前二进制的 `--help` 为命令依据。需要图片或视频时读取命令必须带 `--files`，合并转发必须使用展开后的内部记录而不是外层标题。消息存在但资源未随微信数据提供时，记录 source unavailable；这不算媒体识别失败，也不得根据占位符猜测内容。

语义分析必须使用当前 CLI 提供的信息密度最高、且保留内层发言人和时间的紧凑输出。当前只有 `text/json/jsonl` 时，用 `text` 阅读语义，把 `jsonl --files` 仅作为本地来源坐标和资源索引；只投影判断所需的稳定 ID、时间、sender、回复/转发坐标和资源路径。禁止把完整微信 XML、CDN 参数、头像 URL、重复的 raw content 或整份原始 JSON 送入模型上下文。CLI 后续提供 compact/analysis 输出时优先使用该模式。

## 不可违反的边界

- 原始微信消息是不可改写的 Evidence；Event 是基于 Evidence 的解释。一个 Evidence 可以支持多个 Event，一个 Event 可以有多份 Evidence。
- Event 的语义判断先于 Style：先确认具体工作对象发生了可追踪的业务变化，再由该 Event 派生、复用或确认 Style。Event Candidate 可以在分析态暂时没有 Style；写入后的 active Event 必须关联至少一个 candidate/confirmed Style，或在同一 operation 中从该 Event 创建 candidate Style。
- 正式款号未知不等于对象未知。对象可以由回复链、图片中的独立工作目标、明确回指或连续工作线锚定；只有连具体工作对象都无法指出时，才不得创建 Event，只保存 Evidence／上下文。
- 微信来源身份不因尚未映射真实 Person 而消失。每个稳定 SourceIdentity 都进入身份图；场景职能可以先挂 SourceIdentity，映射 Person 后仍保留原语境。Event 同时保留可确认的来源身份与真实行动者，不能只因 Person unresolved 就丢失发言人、角色或责任线索。
- 款号、工厂单号、客户款号和内部追踪号都是带签发方与使用范围的 Style 标识，不是 Style 本身。字符串不同不能单独拆款，字符串相同也不能跨命名空间单独合款；同一对象链可拥有多个结构化标识。
- 覆盖完整性与判断确定性是两件事。证据不足时保留 candidate Style 或 proposed 关系，但不能用无 Style Event、虚假 candidate Style 或跳过消息簇制造完成结果。
- 摘要、生成顺序、字符串相等和相似度不能单独决定 Event/Style 同一性。一轮会话可以同时交付多个产品对象；批次不是 Style，共享决定可以由一个 Event 同时关联多个成员明确的 Style，后续结果再按对象分支。判断不确定时保留候选关系；材料、参考物、季度或成员未知的批次不能冒充 Style。
- 视频原消息是 Evidence；转写、OCR、关键帧说明和模型摘要是对该 Evidence 的解释，不得伪装成原话。只有音画与聊天上下文能共同闭合“具体对象发生了什么”时，视频才支持 Event；“发送了视频”本身不构成 Event。
- 所有 Workline 数据操作只能经 `workline +query`、`workline +apply`、`workline +style-events`；禁止直接操作飞书 CRUD。不要在本 Skill 中发明第四个 Workline 入口。
- 合并保留旧对象和 canonical 指向；拆分不改写 Evidence。写入动作必须可重试，冲突后先重新查询。

## 必须采用的运行循环

1. 依据用户或已保存配置确定会话与时间范围，建立按来源顺序排列的紧凑输入清单。长会话或合并转发分成连续片段并保留重叠，从首条推进到末条；禁止抽样或只选靠近末尾、款号清楚的消息。重复消息依靠稳定来源键去重。
2. 对当前片段还原来源 envelope 和身份图：解析当前微信账号（本地“我”）、真实 sender、回复目标、转发内部原作者、时间以及 SourceIdentity／Person／RoleClaim 候选，并查询已有身份。Person 未解析时仍可依据本轮发言，为 SourceIdentity 保存带会话、Style 或时间范围、Evidence 与置信状态的场景职能；不要把组织归属、显示名和职能混成一个标签。相关身份已经稳定且足以解释对话时直接继续，不为形式重复分析。
3. 当前片段的稳定来源坐标一旦确定，就批量 `evidence.upsert`；必要的 unresolved SourceIdentity 可以同时写入。Evidence 的保存不等待 Style、Event、图片解释或真实人员映射全部完成。未支持媒体只进入覆盖账本；source unavailable 图片保存其消息 Evidence，但不生成视觉结论。
4. 在当前片段内把连续消息、回复、图片、视频和转发条目聚成证据簇。按簇读取媒体并建立带来源坐标的视觉／视频证据卡，先盘点可持续追踪的具体工作对象，并区分 `work_target`、`reference`、`alternative`、成员明确的 `batch` 与 `unknown`。连续发送的多组“图片/视频/版单”要拆成多个对象槽，同时保留其共同批次范围；“这些/这一批/都”可以覆盖随后补齐的对象槽，不能机械地只指向前一条媒体。视频按 [references/video-evidence.md](references/video-evidence.md) 做音画联合理解：首轮用低成本音轨和稀疏帧定位业务区间，必要时按时间回取原帧或短片段；不得只凭 STT 或单张代表帧下结论。为对象记录只在分析态使用的稳定锚点和依据，再结合回复链、明确回指、批次作用域与媒体演变建立关系图；同一群、同一供应商、时间接近或外观相似都不能单独证明同一对象。
5. 在每个具体对象或成员明确的对象批次内生成 Event Candidate。候选必须同时通过四项正向准入：对象可追踪、业务状态/要求/责任/下一步发生变化、Evidence 能闭合结论、结果可被后续确认/完成/修改/取消/延误或关联。再应用硬性否决：泛化知识/季度策略、寒暄情绪、普通确认、孤立媒体、已被回答的问题、重复表述、无对象动作或不可见媒体推断不单独形成 Event。问答和音画说明收敛为业务结论；同一消息或视频中的事实、反馈与承诺，不同对象或可独立验证结果必须拆分。相同决定确实同时作用于多个明确对象时保留一个共享 Event 并关联全部成员，不为每款复制同义 Event；后续结果只关联实际涉及的分支。
6. 对通过准入的 Event Candidate 及其对象锚点调用 `workline +query`，一次取得已有 Evidence 关系、可能相同的 Event、Style 候选、结构化 Style 标识和已有关系。先判断 Event，再从对象链派生或复用 Style：正式身份不明但具体产品对象成立时创建 candidate Style；版单、图片链、回复链与带来源的编号共同确认对象。不同编号若可能来自设计师、工厂或其他系统，先作为同一对象的候选标识核对，不得因字符串不同默认创建两个 Style；参考物、替代材料和成员未知批次不得建成 Style。
7. 当前片段完成局部覆盖复核后调用 `workline +apply` 增量写入。每个通过准入的 Event 必须关联已有 Style，或在同一 operation 中先 `event.create`、再以 `created_from_event_id` 创建 candidate/confirmed Style 并建立 proposed/confirmed 关系；不得留下长期无 Style 的 active Event。每个 EventEvidenceLink 保存 `support_type` 与 `interpretation`；每个 Style 关系保存对象锚点、回指或款号链形成的可复核 `basis`。一次 operation 聚合本阶段可独立重试的动作，不为每条记录创建零碎 operation。
8. 新发现款号、版单、清晰图片或其他 Style 锚点时，只回溯可能受影响的早期对象链和 candidate Style，以新 operation 补充、升级、合并或修正关系；后段出现的正式编号或结果图片必须沿回复链、视觉对象和工作过程向前传播，不能把编号首次可读时间当作 Style 起点。只有身份 unresolved／inferred、同名冲突，或身份职能会实质改变消息含义时，才触发身份 review；身份上下文不能循环证明同一消息中的事实。
9. 出现 `conflict`、`schema_conflict`、`not_found` 等错误时先重新 `+query`，再修正请求或停留在不确定状态。同一 operation ID 只用于完全相同 payload 的恢复；判断变化必须使用新 ID。
10. 全部片段处理后才执行全范围完成验收：输入清单中的每条记录都必须有证据簇和处置；逐一复核 Event 准入依据、被否决候选、空 Timeline Style、首个/最后一个 Style 节点、跨段回指、未映射身份的场景职能、同款多编号，以及每段可读取视频是否实际完成音画检查。任何 active Event 若没有 proposed/confirmed Style 关系，结果即未完成：要么补建/复用具体 Style，要么把它降级为 Evidence／上下文；任何稳定发言身份若在 Event 中出现却无法回查 SourceIdentity，也视为未完成。
11. 对涉及 Style 的结果调用 `workline +style-events --style-id <id>`，确认当前有效 Event、Evidence 解释、关系、分支与合并后的集合；再与完整覆盖账本对照，确认没有因分段、媒体缺失或晚出现的 Style 锚点漏掉历史。

## 按需阅读的参考资料

- 需要确定范围、身份、回复/转发、Event、时间、Style 或不确定性规则时，阅读 [references/event-style-judgment.md](references/event-style-judgment.md)。
- 范围中包含可读取视频，或文字回复依赖视频内容时，必须阅读 [references/video-evidence.md](references/video-evidence.md)。
- 需要组装请求、选择 query、理解 action、处理响应或错误时，阅读 [references/interface.md](references/interface.md)。

主 Skill 只保留路由和硬约束；参考文档中的判断规则是本 Skill 的具体执行标准。
