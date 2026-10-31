# Web 主题包格式 v1 / Web Theme Package Format v1

本文是 `/web` 自定义主题包 (`themeApiVersion 1`) 的格式与限制说明，中英文同文并列。
This document defines the `/web` custom theme package format (`themeApiVersion 1`) and its
limits; the Chinese and English halves carry the same content.

> **状态 / Status**
>
> 当前已完成并经过验收的是**包格式解析与校验**（JSON/ZIP/图片/路径/预算/内容 hash）。
> 主题管理 API、上传/更新/启停/删除、个人偏好与服务端同步仍以
> `docs/web-theme-implementation-progress.md` 的主进度与验收结果为准。
> 本指南和 `docs/theme-example/` 只描述格式与最小示例，**不代表完整主题功能已经上线**，
> 也不代表管理页或偏好同步已经交付。
>
> What is implemented and verified today is **package parsing and validation**
> (JSON/ZIP/image/path budgets/content hash). Theme management APIs, upload/update/enable/
> delete, per-user preferences and server-side synchronization remain gated on the main
> progress and acceptance record in `docs/web-theme-implementation-progress.md`. This guide
> and `docs/theme-example/` describe the format and a minimal example only; they **do not
> claim that the complete theme feature is live**, nor that the admin page or preference
> sync has been delivered.

---

## 1. 中文

### 1.1 ZIP 结构

ZIP 必须是一个“扁平根”的包，文件名固定且区分大小写：

```text
manifest.json          # 必需
theme.json             # 必需
preview.png            # 可选，与 preview.jpg 互斥
preview.jpg            # 可选，与 preview.png 互斥
assets/<name>.png      # 可选，仅被 backgroundImage 引用的图片
assets/<name>.jpg
assets/<name>.jpeg
```

* 文件必须直接位于 ZIP 根目录。**不能再套一层目录**（例如
  `my-theme/manifest.json` 或 `theme/manifest.json` 都会被拒绝）。
* 不允许目录条目（以 `/` 结尾的条目）、符号链接、设备/管道/套接字等特殊文件。
* 不允许 `.`/`..` 段、绝对路径、盘符、反斜杠、NUL、空路径段；条目名必须是
  ASCII `[A-Za-z0-9._/-]`，最长 255 字节。
* 条目名重复或忽略大小写后冲突都会被拒绝。
* 只接受 store / deflate 两种压缩方法；加密条目被拒绝；CRC 与实际解压长度会被校验。
* 条目数上限 32。除上述文件名外，任何其他条目（CSS、JS、HTML、SVG、字体、README 等）
  都会被拒绝；`assets/` 下只允许 `.png`/`.jpg`/`.jpeg`。

### 1.2 `manifest.json`

顶层必须是 JSON 对象，且只允许下列字段（未知字段、重复键、`null`、尾随数据都会被拒绝）。
`manifest.json` 与 `theme.json` 各自最大 64 KiB。

| 字段 | 类型 | 规则 |
| --- | --- | --- |
| `themeApiVersion` | 整数 | 必须为 `1` |
| `id` | 字符串 | ASCII 小写字母/数字/`-`，3..48 字符，首尾必须是字母或数字；`builtin`、`default`、`admin`、`web` 为保留 id |
| `name` | 字符串 | 1..80 字符（按 rune），无首尾空白，无控制/格式字符 |
| `author` | 字符串 | 0..80 字符，规则同 `name` |
| `description` | 字符串 | 0..400 字符，规则同 `name` |
| `version` | 字符串 | 1..32 字符，仅字母/数字/`.`/`-`，首尾必须是字母或数字 |

`id` 是主题的稳定标识：更新同一主题时必须沿用同一个 `id`，内容变化由内容 hash 区分。

### 1.3 `theme.json`

```json
{
  "variants": { "light": { ... }, "dark": { ... } },
  "appearance": { "radius": 14, "density": "comfortable" },
  "backgroundImage": ""
}
```

* `variants` **必需**，且必须同时包含 `light` 与 `dark` 两个变体；只允许这两个键。
  每个变体必须完整提供 11 个颜色 token，不允许缺项或多余字段。
* 颜色只接受字面量 `#RRGGBB`（不接受 `#RGB`、`rgb()`、`hsl()`、渐变、阴影、CSS 变量或
  任何 CSS 文本）；解析后统一转成大写，因此大小写与 JSON 键顺序不影响内容 hash。
* `appearance` 可选；缺省为 `radius: 14`、`density: "comfortable"`。
  `radius` 取整数 4..24（CSS 像素），`density` 只能是 `compact` 或 `comfortable`。
* `backgroundImage` 可选，缺省或空字符串表示无背景图；非空时必须是归一化的
  `assets/...` 路径，扩展名为 `.png`/`.jpg`/`.jpeg`，并且该文件必须存在于包内。
* 包内每张图片都必须被 `backgroundImage` 引用；未被引用的图片会被拒绝，
  避免上传被当作匿名图床。因此 `backgroundImage` 为空时不得打包任何 `assets/` 图片。
  `preview.png`/`preview.jpg` 是预览图，不属于 `assets/`，不计入该引用规则。

#### 颜色 token 与对比度

| token | 含义 | 对比度要求 |
| --- | --- | --- |
| `bg0` | 页面最底层背景 | — |
| `bg1` | 卡片/面板背景 | — |
| `bg2` | 次级表面 | — |
| `bg3` | 强调表面/悬浮层 | — |
| `text1` | 主文字 | 对 `bg0`、`bg1` 均 ≥ 4.5:1 |
| `text2` | 次要文字 | 对 `bg0`、`bg1` 均 ≥ 3.0:1 |
| `text3` | 辅助/弱化文字 | 对 `bg0`、`bg1` 均 ≥ 3.0:1 |
| `line` | 常规分隔线 | — |
| `line2` | 强分隔线/描边 | — |
| `accent` | 强调色（按钮底色等） | — |
| `accentText` | **页面级链接/强调文字**颜色，不是按钮文字颜色 | 对 `bg0`、`bg1` 均 ≥ 4.5:1 |

`accentText` 只用于正文中的链接与强调文本；按钮标签的前景色由服务端/客户端根据
`accent` 自行选择黑或白，不使用 `accentText`。因此不要用按钮配色反推 `accentText`。
对比度按 WCAG 2.x 相对亮度计算，不合格的包会被整体拒绝，而不是安装后再修复。

### 1.4 预算与图片

| 限制 | 数值 |
| --- | --- |
| ZIP 文件大小 | ≤ 1.5 MiB (1,572,864 字节) |
| 条目数 | ≤ 32 |
| 单个条目解压后 | ≤ 4 MiB |
| 全部条目解压后合计 | ≤ 8 MiB |
| `manifest.json` / `theme.json` 各 | ≤ 64 KiB |
| 单条目压缩比（解压/压缩） | ≤ 100 |
| 图片数量 | 防御上限 3 |
| 单边尺寸 | ≤ 4096 |
| 单张图片像素 | ≤ 4,000,000 |
| 全部图片像素合计 | ≤ 8,000,000 |
| 已安装自定义主题数 | ≤ 20 |
| `dataDir/themes` 磁盘占用 | ≤ 64 MiB（含 current、previous、staging、orphan） |

图片只接受 PNG 与 JPEG：文件头必须与扩展名一致（`.png` / `.jpg` / `.jpeg`），
必须能完整解码且解码尺寸与头部声明一致。GIF、WebP、SVG、字体、WASM 等一律拒绝。

“最多 3 张”是防御性上限；当前 schema 只有**一个** `backgroundImage` 和**一个** preview，
所以一个实际可用的包最多包含 **2 张**图片（1 张背景 + 1 张预览）。
3 张的上限不会为了容纳更多图片而扩展 schema。

### 1.5 公开资源、hash 与隐私

* 图片等资源以内容 hash 作为不可变资源标识；内容变化会产生新的 hash 与新 URL。
* 公开 hash 资源 URL **只包含内容 hash**，不会携带用户媒体 secret、上游凭据、
  用户 id 或播放令牌，任何访客都可以读取。
* 主题资源是可被浏览器/CDN 长期缓存的公开文件。删除或更新主题后，
  **已经进入浏览器缓存或中间缓存的副本无法撤回**。
* 因此不要上传隐私内容、内部截图、带凭据的图片或任何不应公开的素材；
  一旦发布就应视为不可撤销的公开内容。

### 1.6 主题不能改变的东西

* 原 `/admin` 管理页保持内建外观，**不随自定义主题换肤**；主题预览只在受限的
  sandbox iframe 中呈现。
* 播放器与字幕区域是固定域，主题包不能替换播放 UI、媒体解码、字幕渲染或播放 generation。
* 主题包不能影响鉴权、父级控制、虚拟 ID/别名、多实例与去重、分页/partial 语义、
  观看记录或本地 UserData。恢复内建主题的入口必须始终可用。

### 1.7 生成最小示例 ZIP（仅 Python 标准库）

`docs/theme-example/` 内是可直接使用的合法示例（`id` 为 `eio-example`，无图片）。
下列脚本只打包两个 JSON，不添加目录条目、不写入任何图片或非 ASCII 路径：

```bash
cd <repository-root>
python3 - <<'PY'
import pathlib, zipfile

root = pathlib.Path("docs/theme-example")
out = pathlib.Path("/tmp/eio-example-theme.zip")  # 不要写入仓库或部署目录
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
    for name in ("manifest.json", "theme.json"):
        zf.write(root / name, arcname=name)  # 根目录条目，无外层目录
print(out, out.stat().st_size)
PY
```

检查条目名（应只有 `manifest.json`、`theme.json`）：

```bash
python3 -c "import zipfile;print([i.filename for i in zipfile.ZipFile('/tmp/eio-example-theme.zip').infolist()])"
```

生成的 ZIP 只应保留在 `/tmp` 等临时位置；**不要将其提交进 Git**，也不要覆盖
`docs/theme-example/` 中的源 JSON。回归测试
`internal/backend/theme_example_test.go` 会从 `docs/theme-example/` 读取这两个 JSON，
在内存中组包并执行 `ParseThemePackage`，断言 id、双变体、无图片与稳定 hash，
用于防止示例腐坏。

### 1.8 尚未完成

主题管理 HTTP 流程、上传/更新/启停/默认/删除、公开目录、个人偏好与服务端 revision
同步、浏览器端真实视觉与媒体验证等，仍以主进度文档与后续验收为准。本文只冻结包格式。

---

## 2. English

### 2.1 ZIP layout

The package must be flat: fixed file names at the ZIP root (case sensitive).

```text
manifest.json          # required
theme.json             # required
preview.png            # optional, mutually exclusive with preview.jpg
preview.jpg            # optional, mutually exclusive with preview.png
assets/<name>.png      # optional, only images referenced by backgroundImage
assets/<name>.jpg
assets/<name>.jpeg
```

* Files must sit directly at the archive root. **Do not wrap them in another directory**
  (`my-theme/manifest.json` or `theme/manifest.json` are rejected).
* Directory entries (names ending in `/`), symbolic links, devices, pipes, sockets and other
  special files are rejected.
* `.`/`..` segments, absolute paths, drive letters, backslashes, NUL bytes and empty path
  segments are rejected. Entry names are ASCII `[A-Za-z0-9._/-]`, at most 255 bytes.
* Duplicate entry names and names that collide case-insensitively are rejected.
* Only store and deflate compression are accepted; encrypted entries are rejected, and both
  the CRC and the actual inflated length are verified.
* At most 32 entries. Any entry outside the layout above (CSS, JS, HTML, SVG, fonts, README,
  …) is rejected; under `assets/` only `.png`/`.jpg`/`.jpeg` are allowed.

### 2.2 `manifest.json`

The top level must be a JSON object with exactly the fields below (unknown fields, duplicate
keys, `null` values and trailing data are rejected). `manifest.json` and `theme.json` are each
limited to 64 KiB.

| Field | Type | Rules |
| --- | --- | --- |
| `themeApiVersion` | integer | must be `1` |
| `id` | string | ASCII lowercase letters/digits/`-`, 3..48 chars, must start and end with a letter or digit; `builtin`, `default`, `admin`, `web` are reserved |
| `name` | string | 1..80 runes, no leading/trailing whitespace, no control or formatting characters |
| `author` | string | 0..80 runes, same text rules as `name` |
| `description` | string | 0..400 runes, same text rules as `name` |
| `version` | string | 1..32 chars, letters/digits/`.`/`-` only, must start and end with a letter or digit |

`id` is the stable theme identity: an update must keep the same `id`; content changes are
tracked by the content hash.

### 2.3 `theme.json`

```json
{
  "variants": { "light": { ... }, "dark": { ... } },
  "appearance": { "radius": 14, "density": "comfortable" },
  "backgroundImage": ""
}
```

* `variants` is **required** and must contain both `light` and `dark`; no other variant names
  are allowed. Each variant must provide all 11 color tokens, with no missing or extra fields.
* Colors are literal `#RRGGBB` values only (`#RGB`, `rgb()`, `hsl()`, gradients, shadows, CSS
  variables and any CSS text are rejected). Values are normalized to upper case, so letter
  casing and JSON key order never affect the content hash.
* `appearance` is optional and defaults to `radius: 14`, `density: "comfortable"`.
  `radius` is an integer in 4..24 (CSS pixels); `density` is `compact` or `comfortable`.
* `backgroundImage` is optional; missing or empty means no background image. Otherwise it must
  be a normalized `assets/...` path with a `.png`/`.jpg`/`.jpeg` extension, and that file must
  exist in the package.
* Every packaged image must be referenced by `backgroundImage`; unreferenced images are
  rejected so uploads cannot be used as anonymous image hosting. An empty `backgroundImage`
  therefore allows no `assets/` image at all. `preview.png`/`preview.jpg` is the preview image
  and is not part of `assets/`, so it is exempt from that rule.

#### Color tokens and contrast

| Token | Meaning | Contrast requirement |
| --- | --- | --- |
| `bg0` | deepest page background | — |
| `bg1` | card/panel background | — |
| `bg2` | secondary surface | — |
| `bg3` | emphasized surface/overlay | — |
| `text1` | primary text | ≥ 4.5:1 on both `bg0` and `bg1` |
| `text2` | secondary text | ≥ 3.0:1 on both `bg0` and `bg1` |
| `text3` | muted/tertiary text | ≥ 3.0:1 on both `bg0` and `bg1` |
| `line` | regular divider | — |
| `line2` | strong divider/border | — |
| `accent` | accent color (button background, …) | — |
| `accentText` | **page-level link/emphasis text** color, not the button label color | ≥ 4.5:1 on both `bg0` and `bg1` |

`accentText` is only for inline links and emphasized text in page content. Button labels
derive their foreground (black or white) from `accent` on the server/client and never use
`accentText`, so do not infer `accentText` from button styling. Contrast uses WCAG 2.x
relative luminance; a package that fails is rejected as a whole instead of being installed
and repaired later.

### 2.4 Budgets and images

| Limit | Value |
| --- | --- |
| ZIP file size | ≤ 1.5 MiB (1,572,864 bytes) |
| Entry count | ≤ 32 |
| One inflated entry | ≤ 4 MiB |
| All inflated entries | ≤ 8 MiB |
| `manifest.json` / `theme.json` | ≤ 64 KiB each |
| Per-entry compression ratio (inflated/compressed) | ≤ 100 |
| Image count | defensive cap of 3 |
| Image dimension per side | ≤ 4096 |
| Pixels per image | ≤ 4,000,000 |
| Pixels for all images | ≤ 8,000,000 |
| Installed custom themes | ≤ 20 |
| Disk usage under `dataDir/themes` | ≤ 64 MiB (current, previous, staging and orphans) |

Only PNG and JPEG are accepted: magic bytes must match the extension (`.png`/`.jpg`/`.jpeg`),
the image must decode fully, and decoded dimensions must match the declared header. GIF,
WebP, SVG, fonts, WASM and everything else are rejected.

The “3 images” cap is defensive. The current schema has **one** `backgroundImage` and **one**
preview, so a practically usable package can contain at most **2** images (one background plus
one preview). The cap is not a reason to extend the schema.

### 2.5 Public resources, hashes and privacy

* Images and other resources are identified by their content hash; changed content produces a
  new hash and a new URL.
* Public hash resource URLs contain **only the content hash**. They never carry user media
  secrets, upstream credentials, user IDs or playback tokens, and any visitor can read them.
* Theme resources are public files intended for long-lived browser/CDN caching. After a theme
  is deleted or updated, **copies already cached by browsers or intermediaries cannot be
  withdrawn**.
* Do not upload private material, internal screenshots, images containing credentials or
  anything that must not be public; treat a published package as irreversible.

### 2.6 What themes cannot change

* The original `/admin` management UI keeps the builtin appearance and is **not reskinned** by
  custom themes; theme previews are shown only inside a restricted sandboxed iframe.
* The player and subtitle area is a fixed domain. Theme packages cannot replace the playback
  UI, media decoding, subtitle rendering or playback generation.
* Theme packages cannot affect authorization, parental controls, virtual IDs/aliases,
  multi-instance behavior, deduplication, pagination/partial semantics, watch history or local
  UserData. A recovery entry back to the builtin theme must always remain available.

### 2.7 Generating the minimal example ZIP (Python standard library only)

`docs/theme-example/` is a valid example package (`id` is `eio-example`, no images). The script
below packs only the two JSON files, adds no directory entries and writes no images or
non-ASCII paths:

```bash
cd <repository-root>
python3 - <<'PY'
import pathlib, zipfile

root = pathlib.Path("docs/theme-example")
out = pathlib.Path("/tmp/eio-example-theme.zip")  # keep it out of the repository/deploy dir
with zipfile.ZipFile(out, "w", zipfile.ZIP_DEFLATED, compresslevel=9) as zf:
    for name in ("manifest.json", "theme.json"):
        zf.write(root / name, arcname=name)  # root-level entries, no wrapper directory
print(out, out.stat().st_size)
PY
```

Check the entry names (only `manifest.json` and `theme.json`):

```bash
python3 -c "import zipfile;print([i.filename for i in zipfile.ZipFile('/tmp/eio-example-theme.zip').infolist()])"
```

Keep the generated ZIP in a temporary location such as `/tmp`; **do not commit it to Git** and
do not overwrite the source JSON in `docs/theme-example/`. The regression test
`internal/backend/theme_example_test.go` reads those two JSON files from `docs/theme-example/`,
builds an archive in memory, and runs `ParseThemePackage`, asserting the id, both variants,
the absence of images and a stable hash so the example cannot rot.

### 2.8 Not delivered yet

The theme management HTTP flow, upload/update/enable/default/delete, the public catalog, per-user
preferences and server-side revision sync, and real browser visual/media verification remain
subject to the main progress document and later acceptance. This document only freezes the
package format.
