# work-cli

[![License: MIT](https://img.shields.io/badge/License-MIT-yellow.svg)](https://opensource.org/licenses/MIT)
[![Go Version](https://img.shields.io/badge/go-%3E%3D1.23-blue.svg)](https://go.dev/)

[中文版](./README.zh.md) | [English](./README.md)

`work-cli` is the Workline distribution of the Lark/Feishu CLI. It adds the deterministic `workline` interface used by the external [`apparel-skills`](https://github.com/rayson-x/apparel-skills) collection while retaining the underlying Feishu command surface.

Download a platform archive from [GitHub Releases](https://github.com/rayson-x/work-cli/releases). Stable latest-version URLs are `work-cli_windows_amd64.zip`, `work-cli_darwin_arm64.tar.gz`, `work-cli_darwin_amd64.tar.gz`, `work-cli_linux_amd64.tar.gz`, and `work-cli_linux_arm64.tar.gz`. Every release includes `checksums.txt`.

After extraction, verify the required interface with:

```text
work-cli --version
work-cli workline --help
work-cli image --help
```

Image generation and editing use the Workline service through synchronous CLI commands. The CLI has a built-in Workline service endpoint; set `WORKLINE_MEDIA_SERVER_URL` only to override it. A completed `auth login` exchanges the Feishu user token for a local Workline credential automatically. `WORKLINE_MEDIA_API_KEY` remains an optional environment override for managed or non-interactive deployments.

```text
work-cli image +generate --prompt <text> [--reference <path=role>] [--out-dir <directory>]
work-cli image +edit --input <image> --prompt <text> [--out-dir <directory>]
work-cli image +job --task-ref <task_ref> [--wait]
```

`+generate` and `+edit` wait for the asynchronous server task, download every completed output, and return its absolute local path in `data.outputs[].path`.

The project is forked from the official [larksuite/cli](https://github.com/larksuite/cli); the upstream documentation below still applies to the retained Feishu/Lark commands. Use `work-cli` in place of the upstream executable name.

[Install](#installation--quick-start) · [AI Agent Skills](#agent-skills) · [Auth](#authentication) · [Commands](#three-layer-command-system) · [Advanced](#advanced-usage) · [Enterprise](#personal-or-enterprise) · [Security](#security--risk-warnings-read-before-use) · [Contributing](#contributing)

## Why work-cli?

- **Agent-Native Design** — 24 structured [Skills](./skills/) out of the box, compatible with popular AI tools — Agents can operate Lark with zero extra setup
- **Wide Coverage** — 18 business domains, 200+ curated commands, 26 AI Agent [Skills](./skills/)
- **AI-Friendly & Optimized** — Every command is tested with real Agents, featuring concise parameters, smart defaults, and structured output to maximize Agent call success rates
- **Open Source, Zero Barriers** — MIT license, ready to use, just `npm install`
- **Up and Running in 3 Minutes** — One-click app creation, interactive login, from install to first API call in just 3 steps
- **Secure & Controllable** — Input injection protection, terminal output sanitization, OS-native keychain credential storage
- **Three-Layer Architecture** — Shortcuts (human & AI friendly) → API Commands (platform-synced) → Raw API (full coverage), choose the right granularity

## Personal or Enterprise?

| You are... | Recommended path |
| ---------- | ---------------- |
| **An individual developer** — using work-cli in your terminal or with your own AI Agent | Follow the [Quick Start](#installation--quick-start) below |
| **Enterprise IT / ISV** — embedding work-cli into your own Agent or platform, with centralized credentials (database / Vault / config center), unified audit logging, and a restricted command surface | Read [Embed work-cli in your Agent](https://open.larksuite.com/document/mcp_open_tools/feishu-cli/embed-feishu-cli-in-agent) and the [`extension/`](./extension/) packages — extend via a wrapper `main`, no need to modify CLI source |

> 💡 **For AI Agents:** append `.md` to any Open Platform doc URL to fetch it as raw Markdown, e.g. [`embed-feishu-cli-in-agent.md`](https://open.larksuite.com/document/mcp_open_tools/feishu-cli/embed-feishu-cli-in-agent.md).

## Features

| Category      | Capabilities                                                                                                                      |
| ------------- |-----------------------------------------------------------------------------------------------------------------------------------|
| 📅 Calendar   | View, create and update events, invite attendees, find meeting rooms, RSVP to invitations, check free/busy & time suggestions     |
| 💬 Messenger  | Send/reply messages, create and manage group chats, view chat history & threads, search messages, download media                  |
| 📄 Docs       | Create, read, update, and search documents, read/write media & whiteboards                                                        |
| 📁 Drive      | Upload and download files, search docs & wiki, manage comments                                                                    |
| 📝 Markdown   | Create, fetch, patch, and overwrite Drive-native `.md` files                                                                      |
| 📊 Base       | Create and manage tables, fields, records, views, dashboards, workflows, forms, roles & permissions, data aggregation & analytics |
| 📈 Sheets     | Create, read, write, append, find, and export spreadsheet data                                                                    |
| 🖼️ Slides     | Create and manage presentations, read presentation content, and add or remove slides                                              |
| ✅ Tasks      | Create, query, update, and complete tasks; manage task lists, subtasks, comments & reminders                                      |
| 📚 Wiki       | Create and manage knowledge spaces, nodes, and documents                                                                          |
| 👤 Contact    | Search users by name/email/phone, get user profiles                                                                               |
| 📧 Mail       | Browse, search, read emails, send, reply, forward, manage drafts, watch new mail                                                  |
| 🎥 Meetings   | Search live and historical meetings, inspect participants and artifacts, analyze transcripts, manage Minutes, and assist in meetings |
| 🕐 Attendance | Query personal attendance check-in records                                                                                        |
| ✍️ Approval   | Query approval tasks, approve/reject/transfer tasks, cancel and CC instances                                                      |
| 🎯 OKR        | Query, create, update OKRs; manage objective & key results, alignments, indicators and progress.                                  |
| 📋 Project    | Meegle — manage work items, schedules, and data via the standalone [meegle-cli](https://github.com/larksuite/meegle-cli) (install separately) |
| 🔗 Apps       | Create Spark/Miaoda apps, publish HTML/static sites, run cloud generation, and manage access scope                                 |

## Installation & Quick Start

### Requirements

Before you start, make sure you have:

- Node.js (`npm`/`npx`)
- Go `v1.23`+ and Python 3 (only required for building from source)

### Quick Start (Human Users)

> **Note for AI assistants:** If you are an AI Agent helping the user with installation, jump directly to [Quick Start (AI Agent)](#quick-start-ai-agent), which contains all the steps you need to complete.

#### Install

Choose **one** of the following methods:

**Option 1 — From npm (recommended):**

```bash
npx @larksuite/cli@latest install
```

**Option 2 — From source:**

Requires Go `v1.23`+ and Python 3.

```bash
git clone https://github.com/larksuite/cli.git
cd cli
make install

# Install CLI SKILL (required)
npx skills add larksuite/cli -y -g
```

#### Configure & Use

```bash
# 1. Configure app credentials (one-time, interactive guided setup)
work-cli config init

# 2. Log in (--recommend auto-selects commonly used scopes)
work-cli auth login --recommend

# 3. Start using
work-cli calendar +agenda
```

## Quick Start (AI Agent)

> The following steps are for AI Agents. Some steps require the user to complete actions in a browser.

**Step 1 — Install**

```bash
npx @larksuite/cli@latest install
```

**Step 2 — Configure app credentials**

> Run this command in the background. It will output an authorization URL — extract it and send it to the user. The command exits automatically after the user completes the setup in the browser.

```bash
work-cli config init --new
```

**Step 3 — Login**

> Same as above: run in the background, extract the authorization URL and send it to the user.

```bash
work-cli auth login --recommend
```

**Step 4 — Verify**

```bash
work-cli auth status
```

## Agent Skills

| Skill                           | Description                                                                                                    |
| ------------------------------- |----------------------------------------------------------------------------------------------------------------|
| `lark-shared`                   | App config, auth login, identity switching, scope management, security rules (auto-loaded by all other skills) |
| `lark-calendar`                 | Calendar events (create/update), agenda view, free/busy queries, time suggestions, room finding, RSVP replies  |
| `lark-im`                       | Send/reply messages, group chat management, message search, upload/download images & files, reactions          |
| `lark-doc`                      | Create, read, update, search documents (Markdown-based)                                                        |
| `lark-drive`                    | Upload, download files, manage permissions & comments                                                          |
| `lark-markdown`                 | Create, fetch, patch, and overwrite Drive-native Markdown files                                                |
| `lark-sheets`                   | Create, read, write, append, find, export spreadsheets                                                         |
| `lark-slides`                   | Create and manage presentations, read presentation content, and add or remove slides                          |
| `lark-base`                     | Tables, fields, records, views, dashboards, data aggregation & analytics                                       |
| `lark-task`                     | Tasks, task lists, subtasks, reminders, member assignment                                                      |
| `lark-mail`                     | Browse, search, read emails, send, reply, forward, draft management, watch new mail                            |
| `lark-contact`                  | Search users by name/email/phone, get user profiles                                                            |
| `lark-wiki`                     | Knowledge spaces, nodes, documents                                                                             |
| `lark-event`                    | Real-time event subscriptions (WebSocket), regex routing & agent-friendly format                               |
| `lark-meeting`                  | Search live or historical meetings, inspect participants and artifacts, analyze transcripts, manage Minutes, and assist in meetings |
| `lark-whiteboard`               | Whiteboard/chart DSL rendering                                                                                 |
| `lark-openapi-explorer`         | Explore underlying APIs from official docs                                                                     |
| `lark-skill-maker`              | Custom skill creation framework                                                                                |
| `lark-attendance`               | Query personal attendance check-in records                                                                     |
| `lark-approval`                 | Query approval tasks, approve/reject/transfer tasks, cancel and CC instances                                   |
| `lark-workflow-meeting-summary` | Workflow: meeting minutes aggregation & structured report                                                      |
| `lark-workflow-standup-report`  | Workflow: agenda & todo summary                                                                                |
| `lark-okr`                      | Query, create, update OKRs; manage objective & key results, alignments and indicators.                         |

## Authentication

| Command       | Description                                                    |
| ------------- | -------------------------------------------------------------- |
| `auth login`  | OAuth login with interactive selection or CLI flags for scopes |
| `auth logout` | Sign out and remove stored credentials                         |
| `auth status` | Show current login status and granted scopes                   |
| `auth check`  | Verify a specific scope (exit 0 = ok, 1 = missing)            |
| `auth scopes` | List all available scopes for the app                          |
| `auth list`   | List all authenticated users                                   |

```bash
# Interactive login (TUI guides domain and permission level selection)
work-cli auth login

# Filter by domain
work-cli auth login --domain calendar,task

# Recommended auto-approval scopes
work-cli auth login --recommend

# Exact scope
work-cli auth login --scope "calendar:calendar:read"

# Agent mode: return verification URL immediately, non-blocking
work-cli auth login --domain calendar --no-wait
# Resume polling later
work-cli auth login --device-code <DEVICE_CODE>

# Identity switching: execute commands as user or bot
work-cli calendar +agenda --as user
work-cli im +messages-send --as bot --chat-id "oc_xxx" --text "Hello"
```

## Three-Layer Command System

The CLI provides three levels of granularity, covering everything from quick operations to fully custom API calls:

### 1. Shortcuts

Prefixed with `+`, designed to be friendly for both humans and AI, with smart defaults, table output, and dry-run previews.

```bash
work-cli calendar +agenda
work-cli im +messages-send --chat-id "oc_xxx" --text "Hello"
work-cli docs +create --doc-format markdown --content $'<title>Weekly Report</title>\n# Progress\n- Completed feature X'
```

Run `work-cli <service> --help` to see all shortcut commands.

### 2. API Commands

Auto-generated from Lark OAPI metadata, curated through evaluation and quality gates — 100+ commands mapped 1:1 to platform endpoints.

```bash
work-cli calendar calendars list
work-cli calendar events instance_view --params '{"calendar_id":"primary","start_time":"1700000000","end_time":"1700086400"}'
```

### 3. Raw API Calls

Call any Lark Open Platform endpoint directly, covering 2500+ APIs.

```bash
work-cli api GET /open-apis/calendar/v4/calendars
work-cli api POST /open-apis/im/v1/messages --params '{"receive_id_type":"chat_id"}' --data '{"receive_id":"oc_xxx","msg_type":"text","content":"{\"text\":\"Hello\"}"}'
```

## Advanced Usage

### Output Formats

```bash
--format json      # Full JSON response (default)
--format pretty    # Human-friendly formatted output
--format table     # Readable table
--format ndjson    # Newline-delimited JSON (for piping)
--format csv       # Comma-separated values
```

### JSON Output Contract

With `--format json` (the default), success and error envelopes are distinct.

Success goes to **stdout**, exit code `0`:

```json
{ "ok": true, "identity": "user", "data": { "guid": "..." }, "meta": { "count": 1 } }
```

Errors go to **stderr**, non-zero exit code:

```json
{ "ok": false, "identity": "user", "error": { "type": "api", "subtype": "...", "code": 99991679, "message": "...", "hint": "..." } }
```

To check whether a command succeeded, test `ok == true` (or the exit code) — **not** `code == 0`. Unlike raw OpenAPI responses (`{"code": 0, "msg": "ok", ...}`), the success envelope carries no `code` or `msg` field; `code` appears only inside `error` as the upstream OpenAPI code. See [errs/ERROR_CONTRACT.md](errs/ERROR_CONTRACT.md) for the full error taxonomy.

### Pagination

```bash
--page-all                  # Auto-paginate through all pages
--page-limit 5              # Max 5 pages
--page-delay 500            # 500ms between page requests
```

### Dry Run

For commands that may have side effects, preview the request with --dry-run first:

```bash
work-cli im +messages-send --chat-id oc_xxx --text "hello" --dry-run
```

### Schema Introspection

Use schema to inspect any API method's parameters, request body, response structure, supported identities, and scopes:

```bash
work-cli schema
work-cli schema calendar.events.instance_view
work-cli schema im.messages.delete
```

## Security & Risk Warnings (Read Before Use)

This tool can be invoked by AI Agents to automate operations on the Lark/Feishu Open Platform, and carries inherent risks such as model hallucinations, unpredictable execution, and prompt injection. After you authorize Lark/Feishu permissions, the AI Agent will act under your user identity within the authorized scope, which may lead to high-risk consequences such as leakage of sensitive data or unauthorized operations. Please use with caution.

To reduce these risks, the tool enables default security protections at multiple layers. However, these risks still exist. We strongly recommend that you do not proactively modify any default security settings; once relevant restrictions are relaxed, the risks will increase significantly, and you will bear the consequences.

We recommend using the Lark/Feishu bot integrated with this tool as a private conversational assistant. Do not add it to group chats or allow other users to interact with it, to avoid abuse of permissions or data leakage.

To reduce the security risks associated with access token theft, the CLI sends a minimal set of risk-control signals with OpenAPI requests made to exact official Feishu/Lark HTTPS domains. These signals are used to help identify anomalous API activity. This protection is enabled by default. The information sent is limited to:

- Operating system type: macOS, Windows, or Linux
- Device hardware model: for example, Mac17,9

To disable this protection for the current workspace, run:

```bash
work-cli config risk-control off
```

To enable this protection for the current workspace, run:

```bash
work-cli config risk-control on
```

To restore the default policy for the current workspace, run:

```bash
work-cli config risk-control default
```

Please fully understand all usage risks. By using this tool, you are deemed to voluntarily assume all related responsibilities.

## Contributing

Community contributions are welcome! If you find a bug or have feature suggestions, please submit an [Issue](https://github.com/larksuite/cli/issues) or [Pull Request](https://github.com/larksuite/cli/pulls).

For major changes, we recommend discussing with us first via an Issue.

Before opening a PR, see [AGENTS.md](./AGENTS.md) for the local build, test, and PR checklist used by contributors and AI agents.

## License

This project is licensed under the **MIT License**.
When running, it calls Lark/Feishu Open Platform APIs. To use these APIs, you must comply with the following agreements and privacy policies:

- [Feishu User Terms of Service](https://www.feishu.cn/terms)
- [Feishu Privacy Policy](https://www.feishu.cn/privacy)
- [Feishu Open Platform App Service Provider Security Management Specifications](https://open.feishu.cn/document/uAjLw4CM/uMzNwEjLzcDMx4yM3ATM/management-practice/app-service-provider-security-management-specifications)
- [Lark User Terms of Service](https://www.larksuite.com/user-terms-of-service)
- [Lark Privacy Policy](https://www.larksuite.com/privacy-policy)
