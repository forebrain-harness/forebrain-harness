# 组件库

本文档记录 Forebrain Harness 前端的可复用组件。所有组件位于
`frontend/src/components/common/`，样式取值来自设计稿与 `src/assets/main.css`
的 `--forebrain-*` 变量，组件自身不携带调色板以外的颜色。

可视化参照：在开发服务器下打开 `http://localhost:3000/component-gallery.html`
（`frontend/component-gallery.html` 挂载 `src/gallery/ComponentGallery.vue`，
直接渲染真实组件；该页不进构建产物）。

---

## ScopeBadge 作用域标签

显示页面的数据作用域（主代理 / 项目 / 全局），带图标。页头标题旁的作用域标签一律使用它，
不要手写 `<span class="scope-badge">`。

**Props**
- `type`: `'agent' | 'project' | 'global'` — 作用域类型，同时决定图标与配色
- `label`: `string` — 显示文本

**示例**
```vue
<ScopeBadge type="agent" :label="t('scope.agent')" />
<ScopeBadge type="project" :label="t('scope.project')" />
<ScopeBadge type="global" :label="t('scope.global')" data-testid="scope-badge" />
```

额外的属性（如 `data-testid`）经 Vue 的属性穿透落到根元素上。

**设计规范**
| 项 | 值 |
|---|---|
| 高度 | 22px |
| 圆角 | 5px |
| 字号 / 字重 | 11.5px / 600 |
| 图标 | 12×12px（agent: `user-round`，project: `folder-git-2`，global: `globe`） |
| agent | 背景 `--forebrain-brand-soft`，文字 `--forebrain-brand-1` |
| project | 背景 `--forebrain-text`，文字 `#ffffff` |
| global | 背景 `--forebrain-button-alt-bg`，文字 `--forebrain-text-2` |

---

## BadgeComponent 徽标

状态 / 类别 / 数量的短标记。

**Props**
- `variant`: `'default' | 'gray' | 'success' | 'warning' | 'error'` — 变体，默认 `default`

**示例**
```vue
<BadgeComponent>默认</BadgeComponent>
<BadgeComponent variant="gray">灰色</BadgeComponent>
<BadgeComponent variant="success">已连接</BadgeComponent>
<BadgeComponent variant="warning">待确认</BadgeComponent>
<BadgeComponent variant="error">失败</BadgeComponent>
```

**设计规范**
| 项 | 值 |
|---|---|
| 高度 / 圆角 | 20px / 999px |
| 字号 / 字重 | 11px / 500 |
| default | `--forebrain-brand-soft` + `--forebrain-brand-1` |
| gray | `--forebrain-button-alt-bg` + `--forebrain-text-2` |
| success | `--forebrain-success-soft` + `--forebrain-success` |
| warning | `--forebrain-warning-soft` + `--forebrain-warning` |
| error | `--forebrain-bg` + `--forebrain-danger`，1px `--forebrain-danger` 描边 |

---

## SwitchComponent 开关

双态开关，支持 `v-model`。用真实 `<button role="switch">` 实现，状态可被读屏读出。

**Props**
- `modelValue`: `boolean` — 开关状态
- `disabled`: `boolean` — 是否禁用，默认 `false`

**事件**
- `update:modelValue(value: boolean)` — 状态变化时触发

**示例**
```vue
<script setup lang="ts">
import { ref } from 'vue'
const enabled = ref(false)
</script>

<template>
  <SwitchComponent v-model="enabled" aria-label="启用" />
</template>
```

**设计规范**
| 项 | 值 |
|---|---|
| 尺寸 | 32×18px |
| 圆点 | 14×14px，`top/left: 2px`；开启时 `left: 16px` |
| 轨道 | 关闭 `--forebrain-divider-strong`，开启 `--forebrain-brand-1` |
| 过渡 | 背景与圆点位移各 120ms |

---

## CardComponent 内容卡片

页面内容卡片的唯一容器（20 处：设置页 8、项目子页 9、权限页 3）。**不要在各页手写
`class="rounded-2xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-4"`**
——那是落地前的写法，圆角 16px，与设计稿的 8px 不一致。

**Slots**
- `header` — 卡片头部（可选）：一行，左侧标题、右侧操作，行内间距 10px
- 默认插槽 — 卡片主体：子元素之间统一 12px（由 body 的 `gap` 负责）

**示例**
```vue
<!-- 有头部行：标题（可带标签/徽标）在左，操作在右 -->
<CardComponent>
  <template #header>
    <div class="flex w-full flex-wrap items-center justify-between gap-3">
      <div class="flex items-center gap-2">
        <div class="text-[14px] font-medium text-[var(--forebrain-text)]">{{ t('projects.mcpTitle') }}</div>
        <ScopeBadge type="project" :label="t('scope.project')" />
      </div>
      <button type="button" class="forebrain-btn forebrain-btn-ghost text-xs">刷新</button>
    </div>
  </template>
  <p>说明文字</p>
  <div>表单…</div>
</CardComponent>

<!-- 无头部：整块都在主体里 -->
<CardComponent class="mt-3">…</CardComponent>
```

`class` 会透传到卡片根元素，用于卡片自身的**外边距**（`mt-3` / `mb-4` 等）；
`data-testid` 同样透传。

**设计规范**
| 项 | 值 |
|---|---|
| 边框 | 1px `--forebrain-divider`（可用 `--forebrain-card-border` 覆写，见下） |
| 圆角 / 内边距 | 8px / 16px |
| 底色 | `--forebrain-bg`（与 `--forebrain-surface` 同值 #FFFFFF） |
| 头部 | `flex` · 两端对齐 · `gap: 10px`；标题 14px/600（`h3`）· `gap: 8px` |
| 主体 | `display: grid`；`gap: 12px`；`align-content: start` |

**三条使用约定**
1. **间距归卡片**：卡片主体内不要再给首段/相邻块写 `mt-*` 来拉开距离——那是落地前的做法，
   会和卡片的 12px 叠成双倍间距（实测 28px）。需要更大间隔时，把若干块放进一个带
   `space-y-*` 的 wrapper。
2. **换色走变量**：需要给单张卡片换描边色（例如"当前主代理"用品牌色描边）时，不要往
   `class` 上传 Tailwind 边框色——组件的 scoped 规则优先级更高，会压掉它。改用
   `:style="{ '--forebrain-card-border': 'var(--forebrain-brand-border)' }"`。
3. **别用标签名选中卡片**：卡片渲染成 `<div>`（不是 `<section>`），e2e/单测要定位卡片时用
   组件上的 `data-testid`，不要写 `section:not(:last-child)` 这类依赖标签与顺序的选择器。

**不用它的地方**（各有自己的规格）：浮层 / 对话框、聊天气泡、虚线空状态、表格与列表容器
（设计稿对应 `.tb-wrap`）、侧栏与工作台面板、规则编辑面板。

---

## 维护约定

- 新增页面：使用 `<ScopeBadge>`，按页面归属选择 `type`（主代理页 `agent`、项目页 `project`、全局设置 `global`）。
- 修改作用域标签样式：只改 `ScopeBadge.vue`，不要在其他文件的 `<style scoped>` 里覆盖。
- 新增页面内容卡片：用 `<CardComponent>`，不要手写 `rounded-2xl border … p-4`；卡内间距交给卡片。
- 通用组件用法与取值以本文档为准；新组件请一并补上设计规范表。
