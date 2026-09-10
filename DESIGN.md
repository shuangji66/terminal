# Terminal — 设计规范

本设计语言借鉴自 dsh 控制台的设计文档（`dsh/genesis-DESIGN.md`），聚焦「终端」场景：
安静自信、高信息密度但呼吸感充足。无侧边栏、无底栏，顶部标签栏承载会话组织。

## Colors

- **Primary** (#6366F1)：标签激活/交互高亮、主按钮、焦点环 — indigo
- **Primary Hover** (#4F46E5)：主按钮 / 激活态 hover
- **Neutral** (#9C9C9C)：弱化文本、占位符、禁用态
- **Background** (#FAFAFA)：页面背景（浅色）；**Dark** #0B0B0F
- **Surface** (#FFFFFF)：顶栏、弹窗、卡片；**Dark** #111115
- **Text Primary** (#0A0A0A)：标题、正文；**Dark** #EDEDF0
- **Text Secondary** (#6B6B6B)：描述、次级标签；**Dark** #A6A6AD
- **Border** (#E8E8EC)：卡片/分隔细边框；**Dark** #2A2A32
- **Success** (#10B981) / **Warning** (#F59E0B) / **Error** (#EF4444)：语义色

## Typography

- **Display Font**：General Sans（Fontshare）— 标题，紧凑字距（-0.03em）
- **Body Font**：DM Sans（Google Fonts）— 正文与 UI 文本
- **Code Font**：JetBrains Mono — 命令、路径、键值

Type scale：Display 72px / Headline 60px / Section 32px / Subhead 24px / **Body 15px** /
Small 13px / Caption 12px。

## Elevation

- 卡片：1px 边框平铺；hover 时 +2px 上移与 `0 8px 30px rgba(0,0,0,0.08)` 投影。
- 主按钮 hover：`0 4px 12px rgba(99,102,241,0.35)` 辉光 + 上移 1px。
- 顶栏用 `backdrop-blur` 表达层级；弹窗用 `0 10px 40px rgba(0,0,0,0.12)`。
- 焦点态：3px indigo 淡环 `0 0 0 3px rgba(99,102,241,0.12)`。

## Components

- **标签页**：胶囊/圆角条状，激活态 indigo 填充白字 + 辉光；未激活灰字，hover 轻背景。
  「+」按钮新增会话。标签宽度自适应、横向滚动，移动端更紧凑。
- **按钮**：Primary（indigo 填充白字，6px 圆角）、Ghost（无边框，hover 背景）。
  尺寸：small 32px / medium 38px / large 44px。
- **输入框**：1px 边框、6px 圆角、聚焦 indigo 边框 + 3px 淡环。
- **Chip**：胶囊，`black/5` 背景灰字；激活态 indigo 填充白字。
- **顶栏**：56px 高、1px 底边框、`backdrop-blur`；左：logo + 标签条；右：快捷指令 /
  主题 / 语言。
- **辅助键条（移动端）**：紧凑胶囊按键两行，ESC / Tab / Ctrl / Alt / Shift（修饰键
  切换态 indigo 填充）/ 方向键（长按连发）/ `. / - = " '` / 回车。
- **弹窗/确认**：Surface 表面、12px 圆角、`shadow-card`，遮罩 `black/50`。

## Spacing & Radius

- 基础单位 4px；常用间距 8/12/16/24/32px。
- 圆角：6px（按钮/输入框）、8px（面板）、12px（弹窗/卡片）、9999px（胶囊）。

## Do's and Don'ts

- Do 用 indigo 只作为交互元素颜色，不用于装饰/静态文本。
- Do 保持 4px 间距网格。
- Do 保持深浅两种主题对比度；终端配色随主题切换（深色 Catppuccin 风 / 浅色明亮风）。
- Don't 给静态元素加投影 — 投影只用于 hover / 焦点 / 弹层。
- Don't 使用纯黑 #000 / 纯白 #FFF 作文本 — 用调色板值。
- Don't 在同一个视图放置多个主（indigo 填充）按钮。