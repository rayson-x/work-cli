// Copyright (c) 2026 Lark Technologies Pte. Ltd.
// SPDX-License-Identifier: MIT

package config

import "github.com/larksuite/cli/internal/i18n"

type identityMessage struct {
	Escalation string
}

var identityMsgZh = &identityMessage{
	Escalation: "你正在从应用身份切换到用户身份 —— 切换后 AI 将以你的名义在飞书中执行所有操作（读写文档、搜索消息、修改日程等）。⚠️ 请勿将此机器人分享给他人或拉入群聊中使用，以免泄露你的飞书数据。",
}

var identityMsgEn = &identityMessage{
	Escalation: "you are switching from bot-only to user-default — the AI will then act under your Feishu identity for all operations (docs, messages, calendar, etc.). ⚠️ Don't share this bot with others or add it to group chats. It has access to your personal Feishu data.",
}

func identityMessageFor(lang i18n.Lang) *identityMessage {
	if lang.IsEnglish() {
		return identityMsgEn
	}
	return identityMsgZh
}
