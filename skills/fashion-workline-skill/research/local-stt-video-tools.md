# 本地视频理解工具调研（Windows 桌面 Agent）

> 调研日期：2026-08-30。只采用项目官方仓库、官方文档和官方模型卡。这里的“离线”是指模型和运行时下载完成后，推理过程不调用云端 API；首次安装/下载模型仍需要网络。

> 产品约束补充：本地真实视频验证后，默认路径不要求办公电脑运行 VLM/Omni。电脑只做 FFmpeg、场景切分、去重、OCR，以及最多 YOLO26n 量级的可选检测/追踪；语义理解优先交给现有远端多模态 LLM。本报告中的本地 VLM/Omni 仅作为隐私部署或能力上限的备选研究，不代表默认实施方案。实测见 `video-local-evaluation-2026-08-30.md`。

## 结论先行

1. **本地 STT 已经成熟。** 如果先做可上线的中文视频能力，首选 `FunASR（Paraformer/SenseVoice）` 或 `faster-whisper`；如果强调 Windows 单文件/低依赖分发，首选 `whisper.cpp`；`sherpa-onnx` 适合需要 C/C++/C#/Node 多语言嵌入和端侧部署的产品；`Vosk` 适合极轻量、低延迟兜底。
2. **取帧也已经成熟。** `FFmpeg` 能按任意时间点精确 seek、按时间窗口/场景分数筛帧；`PySceneDetect` 能输出场景起止时间和每场景代表帧，并有 Windows 安装包。
3. **广义视频理解不能等同于“STT + 截图”。** 还要覆盖物体、动作、状态变化、画面文字（OCR）、环境声、跨时间推理、视频问答/检索/时间定位。完全本地方案中，`Qwen3-VL` 是视觉视频理解和时间定位最值得试验的模型；`Qwen2.5-Omni` 与 `Qwen3-Omni` 能联合理解视频与音轨，但显存成本从“高”到“服务器级”。
4. **目前没有一个成熟、轻量、完全本地的单体工具，同时做好全部理解能力和可核查证据回链。** 新项目 `Vidify` 是找到的最接近现成实现：本地视频、ASR、OCR/视觉、FAISS 检索、视频 QA、时间线、证据帧、CLI/REST/Agent skill 都已串起来；但项目很新、以 Linux/vLLM 为主，更适合当参考实现和 POC 起点，不宜直接视为成熟 Windows 产品。
5. **最稳妥的产品架构仍是“本地媒体证据层 + 可替换理解后端 + Agent 编排”**；任何模型生成的时间段都必须再由 FFmpeg 回取原帧验证。若允许上传云端，VideoDB/Twelve Labs 的产品形态很接近目标，但明确依赖 API key 和云端处理，不符合“微信视频不出本机”的隐私目标。

## 一、本地 STT 方案对比

| 方案 | 本地/中文 | 时间戳 | CPU/GPU 与 Windows | 接口与分发 | 许可证 / 约束 | 判断 |
|---|---|---|---|---|---|---|
| [whisper.cpp](https://github.com/ggml-org/whisper.cpp) | 官方说明可完全离线运行；`.en` 以外模型均为多语种，覆盖中文 | CLI 默认段级时间；支持 `--output-json-full`，并可用 `--dtw` 计算 token 级时间戳；C API 也暴露时间戳 | 原生 CPU；CUDA、Vulkan、ROCm、OpenVINO；官方列出 Windows（MSVC/MinGW）支持 | `whisper-cli` + C API；模型可量化；官方模型从 75 MiB（tiny）到 2.9 GiB（large-v3），large-v3-turbo Q5 为 547 MiB，见[模型表](https://github.com/ggml-org/whisper.cpp/blob/master/models/README.md) | [MIT](https://github.com/ggml-org/whisper.cpp/blob/master/LICENSE)；CLI 默认只直接读 16-bit WAV，视频音轨通常先用 FFmpeg 转换 | **Windows 分发首选**。依赖少、可封装单一 CLI；中文准确率需拿真实微信视频实测 |
| [faster-whisper](https://github.com/SYSTRAN/faster-whisper) | 模型在本地目录即可离线推理；沿用 Whisper 多语种能力 | 原生段级 `start/end`；`word_timestamps=True` 输出逐词时间 | CPU FP32/INT8；NVIDIA CUDA FP16/INT8；Windows 可用 Python wheel，但 CUDA 需匹配 cuBLAS/cuDNN。PyAV 自带 FFmpeg 库，无需系统安装 FFmpeg | Python 库为主，不是官方独立 CLI；可包装本地 JSON 服务。官方列出 `whisper-ctranslate2`、Windows standalone 等社区集成 | [MIT](https://github.com/SYSTRAN/faster-whisper/blob/master/LICENSE)；首次按模型名加载会从 Hub 下载，也可直接加载本地转换目录 | **快速产品化首选**。性能/内存平衡好，时间戳接口直接；Windows GPU 依赖管理比 whisper.cpp 复杂 |
| [OpenAI Whisper](https://github.com/openai/whisper) | 下载权重后完全本地运行；官方为多语种 ASR，模型评估包含中文 | 返回带 `start/end` 的 segments；`--word_timestamps True` 用 cross-attention + DTW 生成逐词时间；CLI 可输出 `json/jsonl/srt/vtt/tsv` | PyTorch 自动选择 CUDA/CPU；官方 README 给出 Windows 安装 FFmpeg 的方式 | Python CLI + Python API；模型约 39M–1550M 参数，官方估算 VRAM 从约 1 GB 到约 10 GB，见[模型表](https://github.com/openai/whisper#available-models-and-languages) | 代码和权重均 [MIT](https://github.com/openai/whisper#license) | **基准实现**。功能完整但运行时和显存更重，通常不如 faster-whisper/whisper.cpp 适合桌面分发 |
| [FunASR](https://github.com/modelscope/FunASR) | 明确支持离线、流式、端侧；Paraformer-zh 为中英 ASR + 时间戳，SenseVoiceSmall 支持中/英/日/韩/粤语并能输出情绪和音频事件 | Paraformer 支持时间戳；VAD + ASR + CAM++ 可返回句段时间和说话人；CLI 支持 `--timestamps`、JSON、SRT | Python 支持 CPU/CUDA；官方提供 Windows ONNXRuntime 构建说明，也提供 Windows x64 的 GGUF/llama.cpp CPU、Vulkan、CUDA 包 | Python API、`funasr` CLI、本地 OpenAI-compatible server；官方还提供 [MCP/Agent 集成入口](https://github.com/modelscope/FunASR#scale--deploy-the-flagship) | 工具包 [MIT](https://github.com/modelscope/FunASR/blob/main/LICENSE)；**模型许可证不等同于工具包许可证**，官方明确“model licenses vary”，部分自有模型受 [FunASR Model License](https://github.com/modelscope/FunASR/blob/main/MODEL_LICENSE) 约束 | **中文业务首选候选**。除 STT 外还可补环境声/情绪/说话人；上线前逐个确认模型权重许可 |
| [sherpa-onnx](https://github.com/k2-fsa/sherpa-onnx) | 专为本地端侧推理；有多种中文/中英模型（Paraformer、Zipformer、SenseVoice、Whisper 等） | 识别结果暴露 token timestamps；官方 C API 明确“模型不提供时间戳时可为空”，Whisper 还可输出 segment timestamps，见[结果结构](https://k2-fsa.github.io/sherpa/onnx/c-api/html/structSherpaOnnxOfflineRecognizerResult.html) | CPU、CUDA；官方提供 Windows x64/x86/ARM64 预编译库、Windows CUDA wheel/构建路径 | C/C++/Python/C#/Java/Kotlin/Swift/Go/JS 等 API，CLI 和字幕示例；官方字幕 App 标明[本地 CPU、无需联网](https://k2-fsa.github.io/sherpa/onnx/lazarus/download-generated-subtitles.html) | 源码 [Apache-2.0](https://github.com/k2-fsa/sherpa-onnx/blob/master/LICENSE)；具体预训练模型的许可证需单独核对 | **嵌入式/多语言 SDK 首选**。接口面广、Windows 友好；时间戳粒度和可用性取决于所选模型 |
| [Vosk](https://github.com/alphacep/vosk-api) | 官方定义为离线 ASR；支持中文 | C API 可输出逐词 `start/end/conf` JSON；也可输出 partial words | 主要是 CPU；源码可选 CUDA，但官方文档说明不提供 CUDA binaries；Python wheel 支持 Windows x86/x64 | Python/Java/C#/Node/C/C++ 等绑定；官方 `vosk-transcriber` 可直接读取 MP4/M4A，输出 TXT/SRT | API [Apache-2.0](https://github.com/alphacep/vosk-api/blob/master/COPYING)；模型逐个授权。官方中文小模型 42 MB，大模型 1.3 GB，均标 Apache-2.0，见[模型表](https://alphacephei.com/vosk/models) | **超轻量兜底**。42 MB 中文模型适合低配机器，但中文准确率和跨方言能力通常需重点验收 |

### STT 选型建议

- **第一轮真实数据对比**：`FunASR Paraformer/SenseVoice`、`faster-whisper large-v3/turbo`、`whisper.cpp large-v3-turbo Q5` 三路跑同一批微信视频；评价中文专有词、口音、噪声、时间戳偏差、CPU/GPU耗时。
- **默认产品路径**：如果可接受 Python 环境，优先 FunASR 或 faster-whisper；若要随桌面 Agent 发一个轻量二进制，优先 whisper.cpp。
- **不要只保留整段文字**：统一输出 `segments[]`、可选 `words[]`、置信/概率、语言、说话人、音频事件及原始模型信息。中文“逐词”在不同 tokenizer 中可能实际是字或 subword，接口应统一命名为 `tokens/words` 并保留后端原值。

## 二、关键帧与时间点取证

### FFmpeg：必须作为底层能力

[FFmpeg](https://ffmpeg.org/ffmpeg.html) 是本地媒体解码、音轨抽取和按时间取帧的事实标准。官方文档说明：

- `-ss` 可 seek 到指定位置；在转码且默认 `accurate_seek` 开启时，会解码并丢弃目标点前的多余片段，适合 Agent 请求某个精确时间点的原帧。
- `select=between(t,10,20)` 可限定时间窗口，`select='isnan(prev_selected_t)+gte(t-prev_selected_t,10)'` 可按最大间隔兜底抽帧。
- `select='gt(scene,0.4)'` 可按场景变化抽帧；官方建议的常见阈值范围是 0.3–0.5，见 [`select` filter 官方文档](https://ffmpeg.org/ffmpeg-filters.html#select_002c-aselect)。
- `thumbnail` filter 会在一批连续帧中选择代表帧，见[官方说明](https://ffmpeg.org/ffmpeg-filters.html#thumbnail)。
- FFmpeg 本体主要为 LGPL，但具体构建启用的组件可能使整包落入 GPL；分发时必须审计所用 Windows build，见[官方法律说明](https://ffmpeg.org/legal.html)。

### PySceneDetect：场景目录和代表帧

[PySceneDetect](https://www.scenedetect.com/docs/latest/) 提供 CLI 与 Python API，支持 fast cut、fade、adaptive/content/hash/hist 等检测器：

- `scenedetect -i video.mp4 list-scenes save-images` 可输出场景列表，并保存每个场景的开始/中间/结束代表帧。
- API 返回 `(scene_start, scene_end)` 的 `FrameTimecode` 对；`save_images()` 可从每场景保存任意数量图片。
- 官方提供 Windows build；PyAV backend 直接使用容器 PTS，对 VFR 视频时间码最准确，见[后端说明](https://www.scenedetect.com/docs/latest/cli/backends.html)。
- 项目 API 仍在演进，官方建议 pin 到下一个 major 之前；项目为 BSD-3-Clause（见[官方仓库](https://github.com/Breakthrough/PySceneDetect)）。

### 取帧接口应如何暴露给 Agent

底层不要只生成一次性的“关键帧文件夹”，而应暴露可重复调用的时间轴工具：

```text
probe(video)                         -> 时长、fps、time_base、音视频流、旋转/VFR 信息
scenes(video)                        -> [{start,end,score,representative_frames[]}]
frame(video, at=13.820, mode=exact)  -> 原图 + 实际 PTS + 与请求时间的误差
frames(video, from=10, to=18, n=8)   -> 场景变化帧 + 均匀兜底 + 相似帧去重
audio(video)                         -> 本地 PCM/临时音轨
```

这使 Agent 可以从任何线索出发：STT 时间、视觉模型建议的时间段、OCR 命中、环境声事件、聊天回复，随后回取原始帧作为证据。

## 三、广义本地视频理解模型

| 方案 | 能理解什么 | 时间/检索/证据能力 | 本地部署现实性 | 采用判断 |
|---|---|---|---|---|
| [Qwen3-VL](https://github.com/QwenLM/Qwen3-VL) | 物体、动作、空间关系、视频动态、长视频推理；官方强调 32 语种 OCR、长视频、视频 grounding | 架构加入 Text–Timestamp Alignment，官方称可做 timestamp-grounded event localization；本地输入可直接是视频路径或带 `sample_fps` 的帧序列 | Transformers/vLLM/本地 Web UI；2B、4B、8B、32B、30B-A3B 等规格。Windows 可走 Transformers + torchvision/torchcodec，但高性能栈更偏 CUDA/Linux/WSL | **视觉理解最值得做 POC**。2B/4B 可作桌面低配尝试，8B 以上看显存；它不读取视频音轨，仍需 STT/音频模型。模型生成时间戳不是原帧证据，必须二次回取验证 |
| [Qwen2.5-Omni](https://github.com/QwenLM/Qwen2.5-Omni) | 同时感知文字、图像、音频、视频；可利用视频自带音轨，覆盖语音、环境声、音乐和视觉问答 | TMRoPE 用于同步视频与音频时间；适合跨模态问答/推理，但官方接口不直接保证逐条结论带可复核帧证据 | 官方有 3B/7B、本地 Transformers/vLLM、4-bit 版本；资源很重：官方表中 3B BF16 对 15 秒视频理论最少 18.38 GB，7B BF16 为 31.11 GB，实际通常至少再高 20%；未给原生 Windows 路径 | **强能力但不适合作为第一版默认后端**。可在高端 NVIDIA 机器或服务器上做实验；隐私上仍可本地，但部署成本远高于“STT + Qwen3-VL”组合 |
| [Qwen3-Omni](https://github.com/QwenLM/Qwen3-Omni) | 端到端理解文字、图像、语音、环境声、音乐、视频；官方示例覆盖 OCR、object grounding、视频描述、场景转场和 audio-visual QA，并显式支持读取视频音轨 | 强调音画时间对齐和音视频联合问答，也能以本地 vLLM 启动 OpenAI-compatible API；但同样没有现成的“每个 claim 自动回传原帧/音轨”合同 | 目前主力为 30B-A3B。官方 BF16 理论最低显存：Instruct 15 秒视频 78.85 GB、60 秒 107.74 GB；Thinking 为 68.74/95.76 GB。支持 Transformers、vLLM、多卡和本地 Web UI；[Apache-2.0](https://github.com/QwenLM/Qwen3-Omni/blob/main/LICENSE) | **能力覆盖最完整，但只适合服务器级验证**。可作为“单体 Omni 上限组”，不应成为 Windows 桌面第一版默认后端 |
| [VideoLLaMA3](https://github.com/DAMO-NLP-SG/VideoLLaMA3) | 视觉图像/视频理解、长视频理解、文档/表格/OCR；官方 notebook 包含 temporal grounding | 预处理会把采样帧时间写入输入，可做时间定位；没有音频、持久化索引、MCP 或稳定证据回链 API | 2B/7B，本地 Transformers/Gradio；官方推理示例以 CUDA BF16、FlashAttention 2 为主。源码标 Apache-2.0，但 README 又称服务为“research preview, non-commercial only”，商用前必须厘清 | **可做视觉模型对照组**。能力方向吻合，但授权表述和工程成熟度不如 Qwen3-VL 清晰 |
| [InternVL](https://github.com/OpenGVLab/InternVL) | 图像/视频问答与强 OCR；有从小模型到大模型的本地权重 | 官方视频示例主要是均匀采帧并给帧编号，能辅助跨帧推理，但没有音频，也没有第一方的秒级证据/索引合同 | Transformers 本地推理，规格跨度大；高性能路径仍偏 CUDA/Linux | **低配视觉/OCR 备选**，不作为时间定位主后端 |
| [InternVideo3](https://github.com/OpenGVLab/InternVideo/tree/main/InternVideo3) | 面向长视频和 grounded agentic reasoning；官方把视频理解定义为“证据累积—信念更新—工具调用—验证”的闭环 | 明确设计 segmentation、ASR、temporal grounding、search、summarization、verification 等递归取证工具；8B Instruct 权重与初始 Agent 实现已开放 | 本地 Transformers、BF16、8B；示例以 GPU 为主。项目 2026-06 才发布，官方说明论文评测版 Agent 尚待发布；Apache-2.0 | **方向最贴合，但工程仍早期**。值得跟踪/对照其工具循环，不应替代确定性的媒体证据层 |
| [LLaVA-NeXT-Video](https://github.com/LLaVA-VL/LLaVA-NeXT/blob/main/docs/LLaVA-NeXT-Video.md) | 视频问答和描述；官方 demo 按固定数量（如 32）采样帧并读取本地视频 | 主要是整视频 QA，没有官方的持久化语义索引、证据回链或稳定时间定位接口 | 7B/34B 等研究模型，本地 shell/Python demo；[Apache-2.0](https://github.com/LLaVA-VL/LLaVA-NeXT/blob/main/LICENSE)，还要核对基础语言模型权重条款 | **仅作模型/评测参考**。产品集成和时间证据能力不如 Qwen3-VL |
| [Video-LLaMA](https://github.com/DAMO-NLP-SG/Video-LLaMA) | 研究型音视频语言模型，可同时做视觉和音频理解 | 侧重音视频对话；没有成熟的 Agent 工具、语义索引、时间段证据 API | 本地 Python demo，依赖较旧且含多份上游许可证 | **仅借鉴**。不建议作为新 Windows 工具的基础 |

### 为什么仍要保留独立 STT 与原帧证据层

- VLM/Omni 的回答是生成结果，可能遗漏只出现很短时间的细节，也可能把时间段说错。
- 单独 STT 能给出可搜索、可缓存、可局部重跑的音频时间线；SenseVoice 还能补充咳嗽、掌声等音频事件。
- FFmpeg 原帧/原音轨是不可替代的证据。最终每条业务结论应回链到 `video_hash + [start,end] + frame_pts[] + transcript_segment_ids[]`，而不是只保存模型摘要。

## 四、最接近目标的本地 Agent 工程

### Vidify：现成参考实现，而非成熟 Windows 成品

[Vidify](https://github.com/shepnerd/vidify) 是 InternVideo3 官方仓库链接的初始 video-agent 实现，输入支持本地视频、URL 和直播流。它已把目标的大部分产品形态串在一起：

- 生成带 `start/end` 的 timeline，并让事件回链 `asr_segment_ids` 与 `frame_ids`。
- 对转录、帧和 metadata 建 FAISS 索引，提供 evidence-backed QA；另有 OCR、物体、情绪、highlight/clip 导出和直播理解。
- 有 CLI、FastAPI/REST、Web UI、Hermes/OpenClaw Agent skill；VLM 走 OpenAI-compatible endpoint，官方首推本地 vLLM。
- Apache-2.0，Python 3.11+、FFmpeg、yt-dlp；功能是可组合 workflow/skill，允许局部替换后端。

它同时也揭示了现阶段的边界：官方定位仍是 **ASR-first**，不是一个端到端 Omni 模型；README 未给 Windows 原生安装路径，部署脚本和 vLLM 明显偏 Linux/WSL；仓库尚年轻，也没有稳定 MCP 合同。因此最合理的用法是：**直接复用其 timeline/evidence schema、索引与工作流思路，评估能否把 ASR/VLM 适配器换成本项目选定的 FunASR、whisper.cpp、Qwen3-VL，而不是未经验证直接嵌入生产。**

## 五、成熟的云端视频理解产品（形态可借鉴，隐私场景不直接采用）

| 产品 | 已有能力 | Agent 集成 | 为什么不适合当前本地目标 |
|---|---|---|---|
| [VideoDB](https://github.com/video-db) | 上传/存储、语音与视觉索引、语义检索、精确片段、可播放证据、实时流、剪辑；官方定位为 Agent 的 video/audio backend | 官方有 [MCP Agent Toolkit](https://github.com/video-db/agent-toolkit) 和 [Agent Skills](https://github.com/video-db/skills)，可在 Windows 客户端调用并返回 playable clips | MCP/SDK 本地运行不等于模型本地运行；官方技能要求 `VIDEO_DB_API_KEY`，并明确处理是 server-side / VideoDB Cloud。视频需要上传或流式送到其云端 |
| [Twelve Labs](https://docs.twelvelabs.io/docs/concepts/models/pegasus) | Pegasus 视频问答/结构化分段；Marengo any-to-video 语义检索；搜索结果带 `start/end`，Pegasus 1.5 可输出结构化 timestamped segments | REST API + Python/Node SDK；可自己再包 MCP | 官方接口是 `https://api.twelvelabs.io/...` 且需要 API key；属于托管云 API。虽然能力和时间定位成熟，但不满足“视频不出本机” |

这两类产品最值得借鉴的不是模型，而是**结果合同**：检索返回时间段而不是整段摘要；每个命中可立即播放/导出原视频片段；文本、视觉、音频索引共享同一时间轴；Agent 看到的是结构化事件和可回放证据。

## 六、推荐的可落地架构

```text
本地视频
  ├─ 媒体证据层：FFprobe/FFmpeg + PySceneDetect
  │    └─ 可寻址时间轴、场景、任意时间帧、原音轨、内容哈希缓存
  ├─ 音频理解：FunASR 或 faster-whisper（whisper.cpp 作为易分发后端）
  │    └─ STT、说话人、语言、环境声/情绪、时间戳
  ├─ 视觉理解：Qwen3-VL（按需、可关闭）
  │    └─ 物体/动作/状态/OCR/跨时间 QA/候选时间段
  ├─ Omni 上限组：Qwen2.5-Omni / Qwen3-Omni（高配实验，不是必经流程）
  │    └─ 音画联合推理、环境声、只靠模块化流水线难发现的跨模态线索
  └─ Agent 编排层
       ├─ 用聊天上下文提出问题
       ├─ 可由 STT、OCR、场景或 VLM 任一路径触发局部取证
       └─ 结论必须绑定时间段 + 原始关键帧 + 转录片段
```

推荐统一输出：

```json
{
  "media_id": "sha256:...",
  "timeline": {
    "duration": 51.4,
    "transcript_segments": [],
    "audio_events": [],
    "scenes": [],
    "ocr_hits": [],
    "visual_events": []
  },
  "claims": [
    {
      "text": "视频展示某款袖口仍需修改",
      "interval": {"start": 12.4, "end": 17.5},
      "evidence": {
        "transcript_segment_ids": ["stt-008"],
        "frames": [
          {"pts": 13.12, "path": "frames/000013120.jpg"},
          {"pts": 15.64, "path": "frames/000015640.jpg"}
        ]
      },
      "generator": {"model": "...", "confidence": "needs-human-review"}
    }
  ]
}
```

## 七、最小验证计划

1. 用 20–50 条真实微信视频建立金标：逐字稿、关键交付点、可见对象/动作/状态、OCR、环境声、正确时间段和证据帧。
2. 对比三种 STT 后端，不只看文字正确率，还测“业务关键词召回率”和时间偏差。
3. 用 FFmpeg/PySceneDetect 建确定性时间轴与证据接口；先让 Agent 能精确回看，不急于引入大模型。
4. 在有合适 GPU 的机器上测试 Qwen3-VL 2B/4B/8B，任务拆成：对象/OCR、动作与状态变化、跨段推理、时间定位；每项分别评分。
5. 将 Qwen2.5-Omni 作为高配实验后端，将 Qwen3-Omni 作为服务器级能力上限；只验证它们是否在“环境声 + 画面联合推理”上显著优于模块化组合，再决定是否承担显存与部署成本。
6. 所有结论以回取原帧和音轨为准；模型的自然语言时间戳只能作为候选定位，不能直接成为业务事实。

## 最终推荐

- **现在就做**：FFmpeg + PySceneDetect 证据层，STT 后端先接 FunASR 与 faster-whisper，保留 whisper.cpp 接口。
- **下一步 POC**：Qwen3-VL 作为按需视觉 Agent，输入局部时间窗口或场景帧；不要要求它一次吞完所有长视频。
- **现成工程参考**：用 Vidify 跑一轮真实本地视频，重点复用 timeline/evidence schema、FAISS 多模态索引和可组合 workflow；Windows 第一版仍建议自行封装本地进程接口。
- **高配实验**：Qwen2.5-Omni 做音画联合理解对照组；Qwen3-Omni 只作为服务器级上限组。
- **不直接采用**：VideoDB、Twelve Labs（云依赖）；LLaVA-NeXT-Video、Video-LLaMA（研究参考多于产品底座）。VideoLLaMA3/InternVL 可进视觉对照测试，但不承担证据层。
