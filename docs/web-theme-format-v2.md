# Web 主题包格式 v2 / Web Theme Package Format v2

> 状态：**开发中，尚未发布，也尚未部署。** 本文只描述目标格式，不表示任何性能或线上行为已实现。
> Status: **in development, not yet released, not yet deployed.** This document describes the
> intended format only; it makes no claim that any behaviour or performance is implemented or shipped.

## 1. 版本与兼容性 / Versions and compatibility

- 根 `manifest.json` 的 `themeApiVersion` 允许值：`1` 或 `2`。
  Allowed values of `themeApiVersion` in the root `manifest.json`: `1` or `2`.
- v1 主题包的字节、哈希与生成的 CSS 必须保持不变；本轮不产生任何 v1 变更。
  v1 packages, their hashes and their generated CSS must stay byte-identical; no v1 change is made.
- 新版实例读取 v1 与 v2；旧版实例继续只读取 v1，并**拒绝** v2（明确报错，不做降级猜测）。
  New builds read v1 and v2; old builds keep reading only v1 and **reject** v2 with an explicit
  error instead of guessing a downgrade.
- 升级到 v2 **不**意味着把 `manifest.version` 字符串改成 `2`。`version` 是主题作者自己的
  包版本（示例保持 `1.0.0`）；是否支持新块只由 `themeApiVersion` 决定。
  Adopting v2 does **not** mean the `manifest.version` string becomes `2`. `version` is the
  author's own package version (the example keeps `1.0.0`); new blocks are gated only by
  `themeApiVersion`.

## 2. 压缩包结构 / Package layout

- ZIP 根目录直接包含 `manifest.json` 与 `theme.json`，不要再包一层目录。
  The ZIP root contains `manifest.json` and `theme.json` directly; do not wrap them in a folder.
- `docs/theme-example-v2/` 就是可直接压缩的最小合法 v2 示例。
  `docs/theme-example-v2/` is the minimal valid v2 example, ready to be zipped as-is.

## 3. manifest.json

必需字段 / required keys: `themeApiVersion`、`id`、`name`。
可选字段 / optional keys: `author`、`description`、`version`。
v2 的 `id` 是命名空间标识，示例为 `eio-player-theme-example`。
示例不含 `preview` / `assets` 等引用外部文件的字段；v2 也不引入任何外链资源。
The example contains no `preview` / `assets` style fields referencing external files, and v2
introduces no externally linked resources.

## 4. theme.json 校验 / validation

- `variants`（`light` / `dark`）与 `appearance` 语义与 v1 完全一致，继续沿用既有校验。
  `variants` (`light` / `dark`) and `appearance` keep exactly the v1 semantics and validation.
- 拒绝：`null` 值、空字符串、残缺对象（只给一部分键）、未知键；`playerControls` 显式写成
  `null`、`{}` 或只给部分键同样整包拒绝，不做部分生效。
  Rejected: `null` values, empty strings, partial objects (missing keys), unknown keys. A
  `playerControls` written as `null`, `{}` or a partial object rejects the whole package; no
  partial application.
- `playerControls` 是**可选**块；缺失时没有任何强制，播放器在运行时套用固定默认配色
  （`played=#3b82f6`、`buffered=#f3f4f6`、`track=#111827`、`thumb=#3b82f6`），默认值不写入
  存储、不进入内容哈希。
  `playerControls` is **optional**; when absent nothing is enforced and the player applies the
  fixed runtime defaults (`played=#3b82f6`, `buffered=#f3f4f6`, `track=#111827`,
  `thumb=#3b82f6`). Defaults are never written to storage and never enter the content hash.
- 提供了 `playerControls` 就必须给齐四个字段，缺一个即整包拒绝（不做部分生效）。
  If `playerControls` is present, all four keys are required; a missing key rejects the package
  (no partial application).

## 5. playerControls 块 / the playerControls block

四个字段全部为颜色值 / all four keys are colors:
`played`（已播放）、`buffered`（缓冲）、`track`（未播放轨道）、`thumb`（滑块）。
完整示例（可直接叠加在既有 v1 `theme.json` 上）/ complete example (can be added on top of an
existing v1 `theme.json`):

```json
{
  "playerControls": {
    "played": "#3b82f6",
    "buffered": "#f3f4f6",
    "track": "#111827",
    "thumb": "#3b82f6"
  }
}
```

本例沿用 v1 的 `variants` / `appearance` 与已测试的合法取值，只新增上面的块。
This example reuses the v1 `variants` / `appearance` (already covered by tests) and adds only the
block above.

## 6. 颜色写法 / color notation

- v2 的 `playerControls` 输入只接受 **6 位 HEX**（`#RRGGBB`，字母大小写均可），禁止 3 位缩写、`rgb()`、空白或其他CSS值；校验后统一规范化为小写 `#rrggbb` 写入。仅颜色字母大小写不同不改变内容哈希。
  v2 `playerControls` accepts **6-digit HEX** input (`#RRGGBB`, either letter case), not 3-digit shorthand, `rgb()`, whitespace or arbitrary CSS. Valid input is canonicalized to lowercase `#rrggbb` on write; letter case alone does not change the content hash.
- 该大小写规则只适用于 `playerControls`；v1 的 11 个 variant token 仍按 v1 规则写出大写，
  v1 哈希与 CSS 因此保持不变。
  This casing rule applies to `playerControls` only. The 11 v1 variant tokens keep their v1
  upper-case output, so v1 hashes and CSS stay unchanged.

## 7. 可读性与固定播放器外观 / legibility and fixed player chrome

- `played`、`buffered`、`track` 三者**两两**对比度不低于 3:1（默认组合约为
  3.34、4.82、16.12）。
  `played`, `buffered` and `track` must have pairwise contrast of at least 3:1 (the default
  combination measures roughly 3.34, 4.82 and 16.12).
- `thumb` 与 `track` 对比度不低于 3:1（默认组合约 4.82），并且滑块始终带固定黑白描边，
  保证任何底色下可见；`thumb` 与 `played` 允许同色，因为轮廓由程序固定提供。
  `thumb` versus `track` must reach 3:1 (about 4.82 for the defaults), and the thumb always
  keeps a fixed black/white outline so it stays visible on any background. `thumb` may equal
  `played`; the client supplies the outline.
- 播放器控件区固定为深色外观，不随 `light` / `dark` 变体切换。
  The player control area keeps a fixed dark appearance and does not follow the `light` / `dark`
  variants.
- 该块**不**影响字幕样式、字号或位置；字幕风格不在此格式范围内。
  This block does **not** affect subtitle styling, size or position; subtitle styling is out of
  scope for this format.

## 8. 哈希与缓存 / hashing and caching

- v2 的规范化哈希只覆盖实际生效的数据；若 `playerControls` 缺块，则套用固定默认配色，
  且该块不参与哈希计算；同一配置仅颜色字母大小写不同只有一种规范化结果。
  The v2 canonical hash covers only data that takes effect; when `playerControls` is absent the
  fixed defaults apply and the block contributes nothing to the hash, and letter case alone
  yields a single canonical value.
- 主题 CSS 仍通过公开、不可变（public + immutable）的地址提供，主题内容变化时改变哈希。
  Theme CSS is still served from public, immutable URLs, with a new hash when content changes.

## 9. DTO 与后台展示 / DTO and admin surface

- DTO 保留同名 `definition.playerControls` 块，并新增公开的 `themeApiVersion` 字段：内置主题为
  `0`，自定义主题为 `1`（v1）或 `2`（v2）。仅兼容旧服务的新 transport 将缺失字段按 `1`
  处理；未知的正整数版本一律拒绝，而不是静默丢弃或降级。
  The DTO keeps the same-named `definition.playerControls` block and adds a public
  `themeApiVersion` field: `0` for the builtin theme, `1` (v1) or `2` (v2) for custom themes.
  A new transport talking to an older service treats a missing field as `1`; any other positive
  version is rejected rather than silently dropped or downgraded.

- 主题 DTO 中内置主题标记为 `0`，自定义主题为 `1`（v1）或 `2`（v2）。
  In the theme DTO, built-in themes are `0`; custom themes are `1` (v1) or `2` (v2).
- 后台遇到无法解析或不可用的自定义主题时，按未知处理：该 DTO 的数值为 `0` 且
  `builtin=false`，因此仍与真正的内置主题（`builtin=true`）可区分。
  When the admin surface meets an unparsable or unavailable custom theme it treats it as unknown:
  the DTO value is `0` with `builtin=false`, which is still distinguishable from a real built-in
  theme (`builtin=true`).

## 10. 安全边界 / security boundary

- 公开 CSS 路由与安全预览都**不**放宽 CSP；预览可展示经过校验的 v1/v2 内容，固定进度示意不执行脚本或内联样式。
  Neither public CSS routes nor safe previews relax the CSP. Validated v1/v2 packages can be previewed; the fixed progress illustration uses no scripts or inline styles.
- 接受的主题资源按公开、不可变资源提供，**不要上传私人图片或其他敏感内容**；校验器不能识别图片是否属于私人数据。删除主题也无法撤回已有浏览器缓存。不接受远程图片或字体引用，示例没有外链。
  Accepted theme assets are public and immutable: **do not upload private images or sensitive content**. The validator cannot classify privacy, and deletion cannot recall browser caches. Remote image/font references are rejected; this example has no external resources.

## 11. 非目标 / non-goals

- 本文不宣称任何性能改进、扫描优化或上线状态。
  This document claims no performance improvement, scan optimisation, or deployment status.
- 本阶段只验收格式、传输与安全预览；播放器页面接线及浏览器/媒体验收见[实施进度](web-player-progress-theme-implementation-progress.md)。
  This phase verifies format, transport and sandboxed previews only. See [implementation progress](web-player-progress-theme-implementation-progress.md) for player-page wiring and browser/media acceptance.
