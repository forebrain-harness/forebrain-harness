# Plan 001: 前端通用组件样式规范对齐

**Status**: TODO  
**Priority**: HIGH  
**Effort**: M (Medium, 4-6 hours)  
**Risk**: MEDIUM (requires architectural decision on naming convention)  
**Created**: 2026-10-08  
**Base commit**: eafaf46

## Problem

设计稿 `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 定义了一套简短类名的通用组件规范（`.btn`, `.field`, `.badge`, `.sw`, `.card`），但 `frontend/src/assets/main.css` 的实现存在三类问题：

### 1. 命名空间不一致

| 设计稿类名 | 实际实现类名 | 状态 |
|-----------|-------------|------|
| `.btn` | `.forebrain-btn` | ❌ 不一致 |
| `.field` | `.forebrain-field` | ❌ 不一致 |
| `.badge` | 无 | ❌ 缺失 |
| `.sw` | 无 | ❌ 缺失 |
| `.card` | 无（仅有专用组件） | ❌ 缺失 |

### 2. 数值偏差（现有组件 vs 设计稿）

**按钮 `.forebrain-btn` vs 设计稿 `.btn`**:
- `padding`: `0.375rem 0.75rem` (6px 12px) ≠ `0 12px` （垂直 padding 多 6px）
- `border-radius`: `0.75rem` (12px) ≠ `6px` （大 6px）
- `font-size`: `0.8125rem` (13px) ≠ `12.5px` （大 0.5px）

**输入框 `.forebrain-field` vs 设计稿 `.field`**:
- `padding`: `8px 10px` ≠ `0 10px` （垂直 padding 多 8px）
- `border-radius`: `10px` ≠ `6px` （大 4px）
- `textarea.field` 缺失 `line-height: 1.55`

### 3. 缺失的组件和变体

完全缺失：
- `.badge` 及其变体（`.gray`, `.ok`, `.warn`, `.err`）
- `.sw` 开关组件
- `.card` 通用卡片（`.card-h` 标题区）
- `.btn` 变体（`.sm`, `.dgr`, `.link`）

### 4. 现状冲突

- 代码库中 **189 处**使用了 `forebrain-btn` 或 `forebrain-field`
- Vue 组件主要使用 Tailwind CSS 类 + 内联 CSS 变量
- 设计稿中的简短类名在实际代码中**从未被使用**

## Evidence

### 设计稿规范摘录

`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 第 111-171 行：

```css
/* 设计稿定义 */
.btn { height: 32px; padding: 0 12px; border-radius: 6px; border: 1px solid var(--line-2); font-size: 12.5px; font-weight: 500; }
.btn.pri { background: var(--brand); border-color: var(--brand); color: var(--on-brand); }
.btn.sm { height: 26px; padding: 0 9px; font-size: 12px; }
.btn.dgr { color: var(--danger); border-color: var(--danger); }
.btn.link { border: 0; height: auto; padding: 0; color: var(--brand); background: transparent; }

.field { height: 34px; border: 1px solid var(--line-2); border-radius: 6px; padding: 0 10px; font-size: 13px; }
textarea.field { height: auto; padding: 8px 10px; line-height: 1.55; }
.field.mono { font-family: var(--mono); font-size: 12px; }

.badge { height: 20px; padding: 0 7px; border-radius: 999px; font-size: 11px; font-weight: 500; }
.badge.gray { background: var(--soft); color: var(--text-2); }
.badge.ok { background: var(--success-soft); color: var(--success); }
.badge.warn { background: var(--warning-soft); color: var(--warning); }
.badge.err { background: var(--page); color: var(--danger); border: 1px solid var(--danger); }

.sw { width: 32px; height: 18px; border-radius: 999px; background: var(--line-2); }
.sw::after { top: 2px; left: 2px; width: 14px; height: 14px; border-radius: 50%; background: #ffffff; }
.sw[aria-checked="true"] { background: var(--brand); }
.sw[aria-checked="true"]::after { left: 16px; }

.card { border: 1px solid var(--line); border-radius: 8px; padding: 16px; }
.card-h { display: flex; align-items: center; justify-content: space-between; gap: 10px; }
```

### 实际实现摘录

`frontend/src/assets/main.css` 第 667-741 行：

```css
/* 实际实现 */
.forebrain-btn {
  min-height: 2rem;
  padding: 0.375rem 0.75rem;  /* 6px 12px，垂直多 6px */
  border-radius: 0.75rem;     /* 12px，大 6px */
  font-size: 0.8125rem;       /* 13px，大 0.5px */
  font-weight: 500;
  /* ...其他属性 */
}

.forebrain-field {
  border-radius: 10px;        /* 大 4px */
  padding: 8px 10px;          /* 垂直多 8px */
  font-size: 13px;            /* 一致 */
  /* ...其他属性 */
}

/* .badge, .sw, .card 完全缺失 */
```

## Impact

- **视觉不一致性**：按钮和输入框的圆角、间距与设计稿不符，用户界面视觉统一性受损
- **设计系统断层**：设计稿定义的组件无法直接在代码中使用，设计→开发流程断裂
- **缺少语义化组件**：徽标、开关、卡片等高频组件缺失，开发者只能用 Tailwind 临时拼凑
- **命名约定混乱**：`forebrain-` 前缀与设计稿简短类名并存，新人无所适从

## Root Cause Analysis

1. **设计稿与实现脱节**：`app-preview.html` 是产品原型，但未作为实施规范落地到 `main.css`
2. **命名约定未统一**：设计稿用简短类名（`.btn`），实现用前缀命名空间（`.forebrain-btn`），两套体系并存
3. **组件库建设不完整**：只实现了最小可用集（按钮、输入框），badge/switch/card 延期未补齐

## Decision Required

**在编写实施步骤前，必须先确定命名策略（三选一）**：

### 选项 A：采用设计稿的简短类名（`.btn`, `.field` 等）

**优点**：
- 与设计稿 1:1 对应，设计→开发无缝衔接
- 类名简短，代码可读性好
- 符合 BEM、Tailwind 等现代 CSS 约定

**缺点**：
- 需要全局迁移 189 处 `forebrain-btn`/`forebrain-field` 使用
- 可能与第三方库类名冲突（如 Bootstrap、其他 UI 库）
- 打破现有 `forebrain-` 命名空间约定

**迁移成本**：HIGH（需要重写 189 处组件使用）

### 选项 B：保持 `forebrain-` 前缀，同步设计稿的数值规范

**优点**：
- 零迁移成本，不影响现有 189 处使用
- 命名空间隔离，避免类名冲突
- 符合现有代码库约定

**缺点**：
- 设计稿与代码类名永久不一致，需要映射表
- 新增组件需要双重命名（设计稿 `.badge` → 实现 `.forebrain-badge`）
- 类名冗长（`forebrain-btn-primary` vs `.btn.pri`）

**迁移成本**：LOW（只需修正数值，添加缺失组件）

### 选项 C：双轨并行（同时提供两套类名）

**优点**：
- 新代码用简短类名，旧代码继续用前缀类名
- 渐进式迁移，无破坏性变更
- 设计稿直接可用

**缺点**：
- 两套 CSS 规则，维护成本翻倍
- 代码库中两种风格混用，长期技术债
- 容易造成新人困惑

**迁移成本**：MEDIUM（需要编写别名规则并逐步迁移）

## Recommendation

**推荐选项 B**：保持 `forebrain-` 前缀，同步设计稿数值规范。

理由：
1. **最小破坏性**：189 处现有使用无需改动，零回归风险
2. **符合既有约定**：`forebrain-` 命名空间已是项目标准（rail、field、btn 均使用）
3. **可控的设计稿同步**：只需在 `main.css` 中对齐数值，设计稿作为视觉基准而非 API 契约
4. **避免命名冲突**：Tailwind 或其他库引入时不会产生类名碰撞

**代价**：设计稿与代码类名不一致，需要在设计交接时明确映射关系（`.btn` → `.forebrain-btn`）。

## Implementation Steps

**前置条件**：用户确认采用选项 B（保持 `forebrain-` 前缀）。

### Step 1: 修正 `.forebrain-btn` 数值偏差

**文件**：`frontend/src/assets/main.css`（第 667-703 行）

**当前值**：
```css
.forebrain-btn {
  padding: 0.375rem 0.75rem;  /* 6px 12px */
  border-radius: 0.75rem;     /* 12px */
  font-size: 0.8125rem;       /* 13px */
}
```

**修改为**：
```css
.forebrain-btn {
  padding: 0 0.75rem;         /* 0 12px，移除垂直 padding */
  border-radius: 0.375rem;    /* 6px，从 12px 改为 6px */
  font-size: 12.5px;          /* 12.5px，从 13px 改为 12.5px */
}
```

**验证**：
```bash
# 在浏览器开发者工具中检查任一按钮元素
# 期望看到 padding: 0 12px, border-radius: 6px, font-size: 12.5px
```

### Step 2: 修正 `.forebrain-field` 数值偏差

**文件**：`frontend/src/assets/main.css`（第 723-741 行）

**当前值**：
```css
.forebrain-field {
  border-radius: 10px;
  padding: 8px 10px;
  font-size: 13px;
}
```

**修改为**：
```css
.forebrain-field {
  height: 34px;               /* 新增明确高度 */
  border-radius: 6px;         /* 从 10px 改为 6px */
  padding: 0 10px;            /* 移除垂直 padding */
  font-size: 13px;            /* 保持不变 */
}

textarea.forebrain-field {
  height: auto;               /* 新增 textarea 覆盖 */
  padding: 8px 10px;          /* textarea 恢复垂直 padding */
  line-height: 1.55;          /* 新增设计稿要求的行高 */
}
```

**验证**：
```bash
# 检查任一 input.forebrain-field 元素
# 期望：height: 34px, padding: 0 10px, border-radius: 6px

# 检查任一 textarea.forebrain-field 元素
# 期望：padding: 8px 10px, line-height: 1.55
```

### Step 3: 添加缺失的按钮变体

**文件**：`frontend/src/assets/main.css`（在 `.forebrain-btn-ghost` 后添加）

**新增代码**：
```css
/* 小尺寸按钮 */
.forebrain-btn-sm {
  height: 26px;
  min-height: 26px;
  padding: 0 9px;
  font-size: 12px;
}

/* 危险操作按钮（红色边框和文字） */
.forebrain-btn-danger {
  color: var(--forebrain-danger);
  border-color: var(--forebrain-danger);
  background: transparent;
}
.forebrain-btn-danger:hover {
  background: var(--forebrain-danger-soft, #fef3f2);
}

/* 链接样式按钮 */
.forebrain-btn-link {
  border: 0;
  height: auto;
  min-height: auto;
  padding: 0;
  color: var(--forebrain-brand-1);
  background: transparent;
}
.forebrain-btn-link:hover {
  color: var(--forebrain-brand-hover);
  text-decoration: underline;
}
```

**验证**：
在任一 Vue 组件中测试：
```vue
<button class="forebrain-btn forebrain-btn-sm">Small</button>
<button class="forebrain-btn forebrain-btn-danger">Delete</button>
<button class="forebrain-btn-link">Learn more</button>
```

### Step 4: 添加 `.forebrain-badge` 组件

**文件**：`frontend/src/assets/main.css`（在 `.forebrain-field` 块后添加）

**新增代码**：
```css
/* 徽标基础样式 */
.forebrain-badge {
  display: inline-flex;
  align-items: center;
  height: 20px;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

/* 灰色徽标 */
.forebrain-badge-gray {
  background: var(--forebrain-bg-alt);
  color: var(--forebrain-text-2);
}

/* 成功徽标 */
.forebrain-badge-success {
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}

/* 警告徽标 */
.forebrain-badge-warning {
  background: var(--forebrain-warning-soft);
  color: var(--forebrain-warning);
}

/* 错误徽标 */
.forebrain-badge-error {
  background: var(--forebrain-surface);
  color: var(--forebrain-danger);
  border: 1px solid var(--forebrain-danger);
  padding: 0 6px; /* 调整 padding 以抵消 border */
}
```

**验证**：
```vue
<span class="forebrain-badge">Default</span>
<span class="forebrain-badge forebrain-badge-gray">Gray</span>
<span class="forebrain-badge forebrain-badge-success">Success</span>
<span class="forebrain-badge forebrain-badge-warning">Warning</span>
<span class="forebrain-badge forebrain-badge-error">Error</span>
```

### Step 5: 添加 `.forebrain-switch` 组件

**文件**：`frontend/src/assets/main.css`（在 `.forebrain-badge` 块后添加）

**新增代码**：
```css
/* 开关基础样式 */
.forebrain-switch {
  position: relative;
  width: 32px;
  height: 18px;
  flex: none;
  border: 0;
  border-radius: 999px;
  background: var(--forebrain-divider-strong);
  cursor: pointer;
  padding: 0;
  transition: background-color 120ms ease;
}

.forebrain-switch::after {
  content: "";
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: #ffffff;
  transition: left 120ms ease;
}

.forebrain-switch[aria-checked="true"] {
  background: var(--forebrain-brand-1);
}

.forebrain-switch[aria-checked="true"]::after {
  left: 16px;
}

.forebrain-switch:focus-visible {
  outline: 2px solid var(--forebrain-focus-border);
  outline-offset: 2px;
}
```

**验证**：
```vue
<button
  role="switch"
  :aria-checked="enabled"
  class="forebrain-switch"
  @click="enabled = !enabled"
></button>
```

### Step 6: 添加 `.forebrain-card` 组件

**文件**：`frontend/src/assets/main.css`（在 `.forebrain-switch` 块后添加）

**新增代码**：
```css
/* 卡片基础样式 */
.forebrain-card {
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  padding: 16px;
  background: var(--forebrain-surface);
  display: grid;
  gap: 12px;
  align-content: start;
}

/* 卡片标题区 */
.forebrain-card-header {
  display: flex;
  align-items: center;
  justify-content: space-between;
  gap: 10px;
}

.forebrain-card-header h3 {
  margin: 0;
  font-size: 14px;
  font-weight: 600;
  display: flex;
  align-items: center;
  gap: 8px;
}

.forebrain-card-header .forebrain-card-subtitle {
  font-size: 12px;
  color: var(--forebrain-muted-text);
  font-weight: 400;
}
```

**验证**：
```vue
<div class="forebrain-card">
  <div class="forebrain-card-header">
    <h3>Card Title</h3>
    <span class="forebrain-badge">New</span>
  </div>
  <p>Card content goes here.</p>
</div>
```

### Step 7: 添加 CSS 变量缺口

**问题**：设计稿使用的 `--line-2` 等变量在 `main.css` 中不存在。

**文件**：`frontend/src/assets/main.css`（在 `:root` 块中添加）

**新增代码**：
```css
:root {
  /* 现有变量... */
  
  /* 设计稿桥接变量（如果未定义） */
  --line-2: var(--forebrain-divider-strong);
  --soft: var(--forebrain-bg-alt);
  --text-2: var(--forebrain-text-2);
}
```

**验证**：检查新组件是否能正常渲染颜色和边框。

### Step 8: 编写组件文档

**文件**：新建 `frontend/docs/components.md`

**内容**：
```markdown
# Forebrain 通用组件样式指南

本文档记录所有可复用的通用组件类名及其用法。

## 按钮 `.forebrain-btn`

基础按钮，高度 32px，圆角 6px。

### 变体

- `.forebrain-btn-primary`：品牌色主要按钮
- `.forebrain-btn-ghost`：灰色次要按钮
- `.forebrain-btn-danger`：红色危险操作按钮
- `.forebrain-btn-link`：链接样式按钮
- `.forebrain-btn-sm`：小尺寸按钮（26px）

### 示例

\`\`\`vue
<button class="forebrain-btn forebrain-btn-primary">Save</button>
<button class="forebrain-btn forebrain-btn-danger">Delete</button>
<button class="forebrain-btn forebrain-btn-sm">Small</button>
\`\`\`

## 输入框 `.forebrain-field`

统一的表单输入框样式，高度 34px。

### 变体

- `.forebrain-field.font-mono`：等宽字体输入框（用于代码、路径等）
- `textarea.forebrain-field`：多行文本框（自动高度）

### 示例

\`\`\`vue
<input type="text" class="forebrain-field" placeholder="Enter text" />
<input type="text" class="forebrain-field font-mono" placeholder="/path/to/file" />
<textarea class="forebrain-field" rows="4"></textarea>
\`\`\`

## 徽标 `.forebrain-badge`

状态标签和计数徽标，高度 20px。

### 变体

- `.forebrain-badge-gray`：灰色中性徽标
- `.forebrain-badge-success`：绿色成功徽标
- `.forebrain-badge-warning`：黄色警告徽标
- `.forebrain-badge-error`：红色错误徽标

### 示例

\`\`\`vue
<span class="forebrain-badge">Beta</span>
<span class="forebrain-badge forebrain-badge-success">Active</span>
<span class="forebrain-badge forebrain-badge-error">Failed</span>
\`\`\`

## 开关 `.forebrain-switch`

布尔状态切换开关，宽 32px，高 18px。

### 无障碍

必须使用 `role="switch"` 和 `aria-checked`。

### 示例

\`\`\`vue
<button
  role="switch"
  :aria-checked="enabled"
  class="forebrain-switch"
  @click="enabled = !enabled"
></button>
\`\`\`

## 卡片 `.forebrain-card`

内容容器，带边框和圆角。

### 子元素

- `.forebrain-card-header`：卡片标题区，flex 布局
- `.forebrain-card-header h3`：标题
- `.forebrain-card-subtitle`：副标题

### 示例

\`\`\`vue
<div class="forebrain-card">
  <div class="forebrain-card-header">
    <h3>Settings</h3>
    <span class="forebrain-badge">3 changes</span>
  </div>
  <p>Configure your preferences below.</p>
</div>
\`\`\`

## 设计稿映射

设计稿 `app-preview.html` 中的简短类名与实现的映射关系：

| 设计稿类名 | 实现类名 |
|-----------|---------|
| `.btn` | `.forebrain-btn` |
| `.btn.pri` | `.forebrain-btn-primary` |
| `.btn.dgr` | `.forebrain-btn-danger` |
| `.btn.sm` | `.forebrain-btn-sm` |
| `.btn.link` | `.forebrain-btn-link` |
| `.field` | `.forebrain-field` |
| `.field.mono` | `.forebrain-field.font-mono` |
| `.badge` | `.forebrain-badge` |
| `.badge.gray` | `.forebrain-badge-gray` |
| `.badge.ok` | `.forebrain-badge-success` |
| `.badge.warn` | `.forebrain-badge-warning` |
| `.badge.err` | `.forebrain-badge-error` |
| `.sw` | `.forebrain-switch` |
| `.card` | `.forebrain-card` |
| `.card-h` | `.forebrain-card-header` |
\`\`\`
```

### Step 9: 编写视觉回归测试

**文件**：新建 `frontend/src/components/__tests__/ComponentStyles.visual.test.ts`

**目的**：防止未来修改破坏组件尺寸规范。

**内容**：
```typescript
import { describe, it, expect } from 'vitest'
import { mount } from '@vue/test-utils'

describe('Component Styles - Design Spec Compliance', () => {
  it('.forebrain-btn should match design spec dimensions', () => {
    const wrapper = mount({
      template: '<button class="forebrain-btn">Test</button>'
    })
    const button = wrapper.element as HTMLElement
    const styles = window.getComputedStyle(button)
    
    expect(styles.height).toBe('32px')
    expect(styles.paddingLeft).toBe('12px')
    expect(styles.paddingRight).toBe('12px')
    expect(styles.paddingTop).toBe('0px')
    expect(styles.paddingBottom).toBe('0px')
    expect(styles.borderRadius).toBe('6px')
    expect(styles.fontSize).toBe('12.5px')
  })

  it('.forebrain-field should match design spec dimensions', () => {
    const wrapper = mount({
      template: '<input class="forebrain-field" />'
    })
    const input = wrapper.element as HTMLElement
    const styles = window.getComputedStyle(input)
    
    expect(styles.height).toBe('34px')
    expect(styles.paddingLeft).toBe('10px')
    expect(styles.paddingRight).toBe('10px')
    expect(styles.paddingTop).toBe('0px')
    expect(styles.paddingBottom).toBe('0px')
    expect(styles.borderRadius).toBe('6px')
    expect(styles.fontSize).toBe('13px')
  })

  it('textarea.forebrain-field should have line-height', () => {
    const wrapper = mount({
      template: '<textarea class="forebrain-field"></textarea>'
    })
    const textarea = wrapper.element as HTMLElement
    const styles = window.getComputedStyle(textarea)
    
    expect(styles.padding).toBe('8px 10px')
    expect(styles.lineHeight).toBe('1.55')
  })

  it('.forebrain-badge should match design spec dimensions', () => {
    const wrapper = mount({
      template: '<span class="forebrain-badge">Test</span>'
    })
    const badge = wrapper.element as HTMLElement
    const styles = window.getComputedStyle(badge)
    
    expect(styles.height).toBe('20px')
    expect(styles.paddingLeft).toBe('7px')
    expect(styles.paddingRight).toBe('7px')
    expect(styles.borderRadius).toBe('999px')
    expect(styles.fontSize).toBe('11px')
  })

  it('.forebrain-switch should match design spec dimensions', () => {
    const wrapper = mount({
      template: '<button role="switch" class="forebrain-switch" aria-checked="false"></button>'
    })
    const sw = wrapper.element as HTMLElement
    const styles = window.getComputedStyle(sw)
    
    expect(styles.width).toBe('32px')
    expect(styles.height).toBe('18px')
    expect(styles.borderRadius).toBe('999px')
  })
})
```

**运行验证**：
```bash
cd frontend
npm run test:unit -- ComponentStyles.visual.test.ts
```

### Step 10: 浏览器真实渲染验证

**创建测试页面**：`frontend/public/component-gallery.html`

**内容**：所有组件的可视化展示页面（用于人工视觉验证）。

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>Forebrain Component Gallery - Design Spec Verification</title>
  <link rel="stylesheet" href="/src/assets/main.css">
  <style>
    body { padding: 40px; background: var(--forebrain-page-bg); font-family: var(--forebrain-ui-font); }
    .section { margin-bottom: 40px; }
    .section h2 { margin-bottom: 16px; font-size: 18px; }
    .row { display: flex; gap: 12px; flex-wrap: wrap; align-items: center; margin-bottom: 12px; }
    .spec { font-family: var(--forebrain-mono-font); font-size: 12px; color: var(--forebrain-muted-text); }
  </style>
</head>
<body>
  <h1>组件样式验证画廊</h1>
  <p class="spec">对照设计稿 docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html 第 111-171 行</p>

  <div class="section">
    <h2>按钮 .forebrain-btn</h2>
    <div class="row">
      <button class="forebrain-btn">Default (32px × 6px radius)</button>
      <button class="forebrain-btn forebrain-btn-primary">Primary</button>
      <button class="forebrain-btn forebrain-btn-ghost">Ghost</button>
      <button class="forebrain-btn forebrain-btn-danger">Danger</button>
      <button class="forebrain-btn forebrain-btn-sm">Small (26px)</button>
      <button class="forebrain-btn-link">Link Button</button>
    </div>
    <p class="spec">✓ height: 32px, padding: 0 12px, radius: 6px, font: 12.5px</p>
  </div>

  <div class="section">
    <h2>输入框 .forebrain-field</h2>
    <div class="row">
      <input type="text" class="forebrain-field" placeholder="Default input (34px × 6px radius)" style="width: 300px" />
      <input type="text" class="forebrain-field font-mono" placeholder="Monospace" style="width: 200px" />
    </div>
    <textarea class="forebrain-field" rows="3" placeholder="Textarea (line-height 1.55)" style="width: 400px"></textarea>
    <p class="spec">✓ input: height 34px, padding 0 10px, radius 6px | textarea: padding 8px 10px, line-height 1.55</p>
  </div>

  <div class="section">
    <h2>徽标 .forebrain-badge</h2>
    <div class="row">
      <span class="forebrain-badge">Default</span>
      <span class="forebrain-badge forebrain-badge-gray">Gray</span>
      <span class="forebrain-badge forebrain-badge-success">Success</span>
      <span class="forebrain-badge forebrain-badge-warning">Warning</span>
      <span class="forebrain-badge forebrain-badge-error">Error</span>
    </div>
    <p class="spec">✓ height: 20px, padding: 0 7px, radius: 999px, font: 11px</p>
  </div>

  <div class="section">
    <h2>开关 .forebrain-switch</h2>
    <div class="row">
      <button role="switch" aria-checked="false" class="forebrain-switch"></button>
      <button role="switch" aria-checked="true" class="forebrain-switch"></button>
    </div>
    <p class="spec">✓ 32×18px, dot 14×14px, active left: 16px</p>
  </div>

  <div class="section">
    <h2>卡片 .forebrain-card</h2>
    <div class="forebrain-card" style="max-width: 400px">
      <div class="forebrain-card-header">
        <h3>Card Title</h3>
        <span class="forebrain-badge">New</span>
      </div>
      <p>Card content with 16px padding and 8px border-radius.</p>
    </div>
    <p class="spec">✓ padding: 16px, radius: 8px, border: 1px solid var(--line)</p>
  </div>

  <script>
    // 为开关添加交互
    document.querySelectorAll('.forebrain-switch').forEach(sw => {
      sw.addEventListener('click', () => {
        const checked = sw.getAttribute('aria-checked') === 'true'
        sw.setAttribute('aria-checked', String(!checked))
      })
    })
  </script>
</body>
</html>
```

**验证步骤**：
1. 启动开发服务器：`cd frontend && npm run dev`
2. 访问 `http://localhost:5173/component-gallery.html`
3. 使用浏览器开发者工具逐个检查组件尺寸
4. 与设计稿 `app-preview.html` 对照视觉效果

## Done Criteria

执行以下检查，所有项通过即为完成：

1. **数值验证**（浏览器开发者工具）：
   ```bash
   # .forebrain-btn
   padding: 0px 12px
   border-radius: 6px
   font-size: 12.5px
   height: 32px (或 min-height: 32px)
   
   # .forebrain-field
   height: 34px
   padding: 0px 10px
   border-radius: 6px
   
   # textarea.forebrain-field
   padding: 8px 10px
   line-height: 1.55
   
   # .forebrain-badge
   height: 20px
   padding: 0px 7px
   border-radius: 999px (或极大值)
   font-size: 11px
   
   # .forebrain-switch
   width: 32px
   height: 18px
   
   # .forebrain-card
   padding: 16px
   border-radius: 8px
   ```

2. **组件完整性验证**：
   ```bash
   grep -c "\.forebrain-btn-sm" frontend/src/assets/main.css  # 应输出 >= 1
   grep -c "\.forebrain-btn-danger" frontend/src/assets/main.css  # 应输出 >= 1
   grep -c "\.forebrain-btn-link" frontend/src/assets/main.css  # 应输出 >= 1
   grep -c "\.forebrain-badge" frontend/src/assets/main.css  # 应输出 >= 5（基础 + 4 变体）
   grep -c "\.forebrain-switch" frontend/src/assets/main.css  # 应输出 >= 1
   grep -c "\.forebrain-card" frontend/src/assets/main.css  # 应输出 >= 2（card + header）
   ```

3. **测试通过**：
   ```bash
   cd frontend
   npm run test:unit -- ComponentStyles.visual.test.ts
   # 期望：所有测试通过，无失败
   ```

4. **视觉验证**：
   - 访问 `component-gallery.html`，所有组件正常渲染
   - 按钮圆角明显小于之前（从 12px 改为 6px）
   - 输入框圆角也明显变小（从 10px 改为 6px）
   - 徽标、开关、卡片正常显示

5. **文档存在**：
   ```bash
   test -f frontend/docs/components.md && echo "PASS" || echo "FAIL"
   ```

6. **无回归**：
   ```bash
   cd frontend
   npm run build
   # 期望：无 CSS 错误，构建成功
   
   npm run test:unit
   # 期望：现有所有测试仍然通过
   ```

## Out of Scope

以下**不在本计划范围内**，避免范围蔓延：

- ❌ 将现有 189 处 `forebrain-btn` 使用迁移到简短类名（需单独计划）
- ❌ 修改设计稿 `app-preview.html` 的类名（设计稿保持不变，只修改实现）
- ❌ 添加动画效果（hover 过渡等，只实现静态样式）
- ❌ 响应式断点调整（组件尺寸固定，不随屏幕宽度变化）
- ❌ 暗色模式适配（CSS 变量已处理，无需额外工作）
- ❌ 其他未在设计稿中定义的组件（如 modal、dropdown、tooltip）

## Rollback Plan

如果实施后发现严重视觉回归：

1. **立即回滚 CSS 修改**：
   ```bash
   git checkout HEAD~1 -- frontend/src/assets/main.css
   ```

2. **重新构建**：
   ```bash
   cd frontend && npm run build
   ```

3. **删除新增文件**：
   ```bash
   rm -f frontend/docs/components.md
   rm -f frontend/public/component-gallery.html
   rm -f frontend/src/components/__tests__/ComponentStyles.visual.test.ts
   ```

4. **确认回滚**：访问任一页面，检查按钮和输入框是否恢复原状。

## Maintenance Notes

### 未来修改组件样式时

1. **必须同时更新两处**：
   - 实现：`frontend/src/assets/main.css`
   - 文档：`frontend/docs/components.md`

2. **运行视觉回归测试**：
   ```bash
   npm run test:unit -- ComponentStyles.visual.test.ts
   ```

3. **更新 `component-gallery.html`**：如果新增变体，添加到画廊页面。

### 设计稿与实现的同步

- 设计稿 `app-preview.html` 使用**简短类名**（`.btn`, `.field` 等）
- 实现使用 **`forebrain-` 前缀**（`.forebrain-btn`, `.forebrain-field` 等）
- 映射关系记录在 `frontend/docs/components.md` 的"设计稿映射"章节
- 新人接手时，务必先阅读该章节

## Related Plans

- 无依赖（本计划可独立执行）
- 后续可能的计划：
  - Plan 002: 渐进式迁移现有代码到新组件类名
  - Plan 003: 添加 Storybook 组件文档站

## References

- 设计稿：`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html` 第 111-171 行
- 实现文件：`frontend/src/assets/main.css` 第 667-874 行
- CSS 变量定义：`frontend/src/assets/main.css` 第 5-114 行

---

**计划编写完成时间**：2026-10-08  
**预计实施时间**：4-6 小时  
**风险等级**：MEDIUM（需要用户确认命名策略）
