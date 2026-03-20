# Security Policy

## Supported Versions

| Version | Supported |
| ------- | --------- |
| Latest (`main`) | ✅ |
| Older commits | ❌ |

Only the latest version on the `main` branch receives security fixes. Please update before reporting.

---

## Reporting a Vulnerability

**请勿通过 GitHub Issue 公开披露安全漏洞。**

如发现安全问题，请通过以下方式私下联系：

- **GitHub Security Advisories**：[提交私密报告](https://github.com/ArizeSky/Emby-In-One/security/advisories/new)
- 或通过 GitHub 私信联系仓库维护者

报告时请尽量包含：

1. 漏洞类型（如 RCE、XSS、配置泄露等）
2. 复现步骤（最小可复现示例）
3. 影响范围（哪些版本、哪些配置下受影响）
4. 可能的修复建议（可选）

收到报告后，维护者将在 **7 个工作日内**回复确认，并在修复后发布 Advisory 致谢。

---

## Security Considerations

使用本项目前，请了解以下安全事项：

### 管理面板

- 管理面板（`/admin`）受用户名/密码保护，默认由 `config/config.yaml` 中的 `admin` 字段配置。
- **强烈建议**使用强密码，并避免将管理面板直接暴露在公网，推荐通过反向代理限制访问来源 IP。

### 上游凭据存储

- 上游服务器的用户名、密码及 API Key 以**明文**存储在 `config/config.yaml` 中。
- 请确保配置文件权限设置正确（建议 `chmod 600 config/config.yaml`），避免其他用户读取。
- Docker 部署时，确保挂载目录不对外暴露。

### 代理与网络

- 本项目会将客户端请求（包括认证 Token）透传给上游 Emby 服务器。
- 配置的网络代理（`proxies`）会经手认证凭据，请只使用可信代理。
- 建议在本项目前部署 HTTPS 反向代理（如 Nginx），避免凭据在传输中明文暴露。

### 账号封禁风险

- 本项目通过模拟 Emby 客户端行为与上游通信（UA 伪装、Passthrough），存在被上游服务器识别并**封禁账号或 API Key** 的风险。
- 此为使用层面的已知风险，不属于安全漏洞范畴，不在本政策处理范围内。

### 日志

- 运行日志可能包含请求 URL、部分请求头等信息，请妥善保管日志文件，避免泄露上游地址或 Token 信息。
- 日志文件位于 `log/` 目录，Docker 部署时对应挂载路径。

---

## Scope

以下属于本安全策略的处理范围：

- 管理面板认证绕过
- 配置文件或凭据信息泄露（通过 API 或日志）
- 服务端请求伪造（SSRF）
- 远程代码执行（RCE）
- 任意文件读写

以下**不在**范围内：

- 上游 Emby 服务器自身的安全问题
- 因用户自行配置不当（如弱密码、公网暴露管理面板）导致的问题
- 账号被上游封禁（属已知使用风险）

---

## Acknowledgements

感谢所有负责任地披露安全问题的研究者，修复后将在 Security Advisory 中致谢。
