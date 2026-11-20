# gssh 应用场景与产品路线

## 定位

gssh 面向**需要从程序可靠地调用多台机器的自动化客户端**：AI Agent、CI/CD Runner、运维脚本和开发者本地工具。它把 SSH 连接生命周期（建立、复用、断线重连、端口转发）交给常驻 daemon 管理，同时以 ssh/scp 兼容的命令行提供结构化输出。远端只需标准 SSH 服务，适合不能安装 Agent 的生产环境。

交互式终端工具（tmux、mosh）和大规模配置编排工具（Ansible、Parallel-SSH）仍有明确边界；gssh 的优势是单机安装、按需启动、低延迟复用、JSON/流式输出以及 API 可预测。

## 典型用户与场景

| 用户 | 场景 | 当前价值 | 关键缺口 |
| --- | --- | --- | --- |
| AI Agent/LLM 工具 | 反复执行检查、部署、收集日志；命令可能持续数分钟 | JSON 结果、超时、流式帧、自动重连、审计 | 缺少取消/任务 ID、stdin、环境和工作目录；错误码/错误类型不够稳定 |
| CI/CD Runner | 构建、发布、健康检查、失败重试 | 非交互、连接复用、SFTP、端口转发 | 缺少幂等重试策略、并行多主机、秘密从环境/文件安全注入、结构化日志关联 |
| SRE/运维脚本 | 临时登录多台主机、拉日志、执行修复 | ssh 风格 CLI、会话持久、断线恢复 | 缺少 host 别名/配置文件、批量 fan-out、权限与策略控制、可观测指标 |
| 开发者 | 本地开发环境 + 测试机之间复制文件、访问内网服务 | local 会话、sync、-L/-R 转发 | 缺少 shell 补全、友好诊断、交互式 PTY（明确边界） |

## 现有痛点与改进优先级

### P0（直接影响可靠性和自动化采用）

1. **可取消的异步任务（已实现流式 exec）**：流式 exec 分配 task ID，提供 `gssh tasks` 和 `gssh cancel`；客户端断开时清理本地进程组或远端 SSH session。transfer/forward 的任务化仍可继续扩展。
2. **统一错误模型（已实现）**：失败返回稳定的 JSON（`code`, `message`, `retryable`, `session`, `task_id`），CLI 同时保留人类可读文本。
3. **安全凭据入口（已实现基础能力）**：支持 `--password-stdin`，禁止 SSH 密码进入 argv；凭据 FD/外部命令和审计脱敏仍可扩展。
4. **连接和命令超时分离（已实现基础能力）**：`--connect-timeout` 控制 SSH 建连/认证，`-t` 控制命令执行；idle/transfer 专用超时仍可扩展。

### P1（扩大适用场景）

5. **批量并行执行（已实现基础能力）**：`gssh batch -f hosts.txt --parallel 16 -- "uname -a"`，每台主机独立超时、按输入顺序聚合 JSON，失败汇总。
6. **配置档案和 Host 别名（已实现单跳能力）**：读取 `~/.ssh/config` 的 Host/HostName/User/Port/IdentityFile/ProxyJump，命令行参数优先；多跳和独立 gssh 配置格式仍可扩展。
7. **命令上下文（已实现）**：支持 `--cwd`、`--env KEY=VALUE`、`--stdin` 和 `--stdin-file`；流式 stdin 暂不支持以避免输入生命周期歧义。
8. **可观测性**：`gssh status --json` 提供会话延迟、重连次数、活动任务、转发状态；可选 Prometheus endpoint，审计记录 request/task/session ID。
9. **传输增强（已实现基础续传）**：SFTP 对严格前缀文件自动断点续传；校验和、限速、压缩选项仍可扩展。

### P2（体验和生态）

10. **Shell 完成与机器可读帮助（已实现）**：`gssh completion bash|zsh|fish` 生成补全脚本，`gssh help --json` 描述命令、参数和 capabilities，便于 IDE/Agent 发现能力。
11. **插件化认证/代理**：PKCS#11、硬件密钥、ProxyCommand/JumpHost；保持远端零安装。
12. **受控 TTY（暂不启用）**：当前继续保持纯非交互模型；需要交互式 shell 时使用 OpenSSH/tmux/mosh，后续若实现必须显式 opt-in 并定义审计和会话边界。

## 与相邻工具的边界

- **OpenSSH**：功能最全、交互体验最佳；gssh 通过 daemon 复用和 JSON/重连降低 Agent 集成成本，不替代复杂 SSH 配置。
- **tmux**：解决终端复用和人工接管；gssh 解决程序化 RPC、结构化结果和生命周期管理。
- **mosh**：适合高延迟网络下的交互终端；gssh 侧重可审计、可重试的命令和转发。
- **Ansible/Parallel-SSH**：适合声明式批量编排；gssh 保持轻量单二进制，可作为其底层执行器。

## 建议的验证指标

- 重复命令的中位延迟相对新建 SSH 降低比例（目标 ≥50%）。
- 网络抖动下命令成功率、自动重连恢复时间（P95）。
- Agent 解析失败率（结构化错误/输出）。
- 批量执行吞吐与失败隔离；秘密出现在进程表/审计日志中的次数应为 0。
