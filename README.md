# komari-agent

## 配置方式

agent 参数可以通过命令行参数、环境变量或 JSON 配置文件传入。

最小启动示例：

```bash
./komari-agent --endpoint "https://example.com" --token "your-token"
```

使用环境变量：

```bash
export AGENT_ENDPOINT="https://example.com"
export AGENT_TOKEN="your-token"
./komari-agent
```

使用 JSON 配置文件：

```bash
./komari-agent --config ./config.json
```

`config.json` 示例：

```json
{
  "endpoint": "https://example.com",
  "token": "your-token",
  "interval": 3,
  "disable_auto_update": false,
  "disable_web_ssh": false,
  "ignore_unsafe_cert": false
}
```

配置优先级从低到高为：默认值、命令行参数、环境变量、JSON 配置文件。

常用配置项：

表中支持版本表示该参数本身首次在发布 tag 中出现；环境变量和 JSON 配置文件方式从 `1.1.33` 起支持，早于最早 tag 的参数记为 `0.0.9`。

| JSON 字段 | 环境变量 | 命令行参数 | 说明 | 支持版本 |
| --- | --- | --- | --- | --- |
| `endpoint` | `AGENT_ENDPOINT` | `--endpoint`, `-e` | 面板地址 | `0.0.9` |
| `token` | `AGENT_TOKEN` | `--token`, `-t` | agent token | `0.0.9` |
| `interval` | `AGENT_INTERVAL` | `--interval`, `-i` | 数据采集间隔，单位秒 | `0.0.9` |
| `disable_auto_update` | `AGENT_DISABLE_AUTO_UPDATE` | `--disable-auto-update` | 禁用自动更新 | `0.0.9` |
| `disable_web_ssh` | `AGENT_DISABLE_WEB_SSH` | `--disable-web-ssh` | 禁用远程控制 | `0.0.9` |
| `ignore_unsafe_cert` | `AGENT_IGNORE_UNSAFE_CERT` | `--ignore-unsafe-cert`, `-u` | 忽略不安全证书 | `0.0.9` |
| `include_nics` | `AGENT_INCLUDE_NICS` | `--include-nics` | 仅统计指定网卡，逗号分隔 | `0.0.22` |
| `exclude_nics` | `AGENT_EXCLUDE_NICS` | `--exclude-nics` | 排除指定网卡，逗号分隔 | `0.0.22` |
| `include_mountpoints` | `AGENT_INCLUDE_MOUNTPOINTS` | `--include-mountpoint` | 仅统计指定挂载点，分号分隔 | `0.1.0` |
| `month_rotate` | `AGENT_MONTH_ROTATE` | `--month-rotate` | 流量统计每月重置日期，`0` 为禁用 | `0.1.0` |
| `auto_discovery_key` | `AGENT_AUTO_DISCOVERY_KEY` | `--auto-discovery` | 自动发现密钥 | `1.0.40` |
| `custom_dns` | `AGENT_CUSTOM_DNS` | `--custom-dns` | 自定义 DNS 服务器 | `1.0.80` |
| `enable_gpu` | `AGENT_ENABLE_GPU` | `--gpu` | 启用详细 GPU 监控 | `1.0.80` |
| `protocol_version` | `AGENT_PROTOCOL_VERSION` | `--protocol-version` | 上报协议版本，默认 `2` | `1.2.10` |
| `disable_compression` | `AGENT_DISABLE_COMPRESSION` | `--disable-compression` | 禁用 v2 传输压缩 | `1.2.10` |
| `prefer_ip_version` | `AGENT_PREFER_IP_VERSION` | `--prefer-ip-version` | 优先使用 IP 版本，可选 `4` 或 `6` | 未发布 |
| `update_repo` | `AGENT_UPDATE_REPO` | `--update-repo` | 自定义更新仓库，格式 `owner/repo` | 未发布 |

完整参数可运行：

```bash
./komari-agent --help
```

详见 `cmd/flags/flag.go` 及 `cmd/root.go`

## 自定义更新仓库

默认情况下 Agent 从 `gavinv9/komari-agent` 检查更新。如需指向 Fork 或私有仓库，可通过以下方式配置：

- 命令行：`--update-repo myorg/myagent`
- 环境变量：`AGENT_UPDATE_REPO=myorg/myagent`
- JSON 配置：`"update_repo": "myorg/myagent"`

安装脚本同样支持 `--install-repo` 参数，会同时设置下载源和 Agent 自更新仓库：

```bash
wget -qO- https://raw.githubusercontent.com/myorg/myagent/refs/heads/main/install.sh | sudo bash -s -- --install-repo myorg/myagent -e https://panel.example.com -t your-token
```

> 安装脚本默认从原版仓库 `gavinv9/komari-agent` 下载二进制。

> 仓库 slug 必须匹配 `owner/repo` 格式（仅允许字母、数字、点、连字符、下划线），无效值会被拒绝。

## 自更新安全校验

Agent 自更新时会对下载的二进制进行完整性校验，防止传输损坏或篡改。

| 校验阶段 | 机制 | 说明 | 公钥 | 状态 |
|---------|------|------|------|------|
| **Stage 1** | `checksums.txt` 哈希校验 | 下载资产后比对 SHA256，无 checksums 文件则跳过更新 | 不需要 | ✅ 已实现 |
| **Stage 2** | GPG 签名验证 | 使用嵌入的公钥验证 `checksums.txt` 签名，防止 GitHub 账号被盗后的恶意替换 | 需要 | ❌ 未实现 |

### Stage 1（当前）

- 稳定版：`go-github-selfupdate` 库自动查找 release 中的 `checksums.txt` 并校验
- Snapshot：代码显式查找 checksums 资产，缺失时记录警告并跳过
- CI 工作流：`release.yml` 和 `snapshot.yml` 均会在发布后生成 `checksums.txt` 并上传

### Stage 2（计划中，未实现）

> ⚠️ **Stage 2 尚未实现**。当前仅完成 Stage 1 的 checksums 校验。

Stage 2 将使用 GPG 签名验证 `checksums.txt` 的真实性，防止攻击者替换 release 资产。

**GPG 密钥分发方案**

| 密钥类型 | 存储位置 | 用途 |
|---------|---------|------|
| GPG 私钥 | `komari-agent` 仓库的 GitHub Secrets | CI 签名发布资产 |
| GPG 公钥 | 嵌入 agent 二进制 (`update/release_public.key`) | 运行时验证签名 |

**实现步骤（待完成）**

1. 生成 GPG 密钥对：
   ```bash
   gpg --full-generate-key  # 选择 RSA 4096
   gpg --armor --export-secret-keys YOUR_KEY_ID > private.key
   ```
2. 私钥存入 GitHub Secrets：
   - 仓库：`gavinv9/komari-agent`
   - 路径：Settings → Secrets and variables → Actions → New repository secret
   - Name: `GPG_PRIVATE_KEY`
   - Value: `private.key` 的完整内容
3. 公钥放入 `update/release_public.key`（已通过 `go:embed` 嵌入）
4. CI 工作流添加签名步骤：`gpg --detach-sign --armor checksums.txt`
5. Agent 代码添加验证 `checksums.txt.sig` 的逻辑

## 对外网络请求

| 用途 | 地址 | 触发方式 |
|---|---|---|
| 自更新 | `api.github.com`（默认 `gavinv9/komari-agent`） | 定时检查 |
| IPv4 检测 | `visa.cn/cdn-cgi/trace`、`qualcomm.cn/cdn-cgi/trace`、`toutiao.com/...`、`edge-ip.html.zone/geo`、`vercel-ip.html.zone/geo`、`ipv4.ip.sb`、`api.ipify.org` | 启动时多源冗余 |
| IPv6 检测 | `v6.ip.zxinc.org`、`api6.ipify.org`、`ipv6.icanhazip.com`、`api-ipv6.ip.sb` | 启动时多源冗余 |
| 卸载帮助 | `komari-document.pages.dev/faq/uninstall.html` | Windows 弹窗 |

## 原始项目

- 服务端：[komari-monitor/komari](https://github.com/komari-monitor/komari)
- Agent：[komari-monitor/komari-agent](https://github.com/komari-monitor/komari-agent)
- Lite 服务端：[nuomiiiii/komari](https://github.com/nuomiiiii/komari)
- Lite Agent：[nuomiiiii/komari-agent](https://github.com/nuomiiiii/komari-agent)