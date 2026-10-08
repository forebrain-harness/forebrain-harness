# Frontend Design Alignment Plan

**状态**: 已实施（2026-10-08，待 owner 审阅工作区改动）  
**优先级**: HIGH  
**基准 commit**: `eafaf46`  
**设计稿**: `/Users/doudou/Downloads/Forebrain Harness 全站预览.html`  
**预计工作量**: 8-12 小时  

---

## 执行摘要

本计划修复前端代码与设计稿的所有不一致项，分为 4 个独立的实施阶段：

1. **Phase 1**: 作用域标签修复（P0，必须修复）
2. **Phase 2**: 视觉尺寸对齐（P0+P2，细节优化）
3. **Phase 3**: 通用组件实现（P1，设计系统完整性）
4. **Phase 4**: 技术债清理（P3，代码质量）

每个阶段独立可执行，互不依赖。

---

## Phase 1: 作用域标签修复

**目标**: 修复作用域标签的 6 个问题（发现 3.1-3.6）  
**工作量**: 3-4 小时  
**风险**: LOW  

### 问题清单

| # | 问题 | 影响 |
|---|------|------|
| 3.1 | 3 个页面完全缺失标签 | HIGH |
| 3.2 | 所有标签颜色错误 | HIGH |
| 3.3 | 所有标签缺失图标 | MEDIUM |
| 3.4 | 样式属性不符（7 处差异） | MEDIUM |
| 3.5 | 80+ 行重复样式代码 | MEDIUM |
| 3.6 | 无独立可复用组件 | MEDIUM |

### 实施步骤

#### Step 1.1: 创建 ScopeBadge 组件

**文件**: `frontend/src/components/common/ScopeBadge.vue`

```vue
<template>
  <span :class="['scope-badge', `scope-badge--${type}`]">
    <component :is="iconComponent" class="scope-badge-icon" aria-hidden="true" />
    <span class="scope-badge-text">{{ label }}</span>
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'
import { UserRound, FolderGit2, Globe } from 'lucide-vue-next'

type ScopeType = 'agent' | 'project' | 'global'

interface Props {
  type: ScopeType
  label: string
}

const props = defineProps<Props>()

const iconComponent = computed(() => {
  switch (props.type) {
    case 'agent':
      return UserRound
    case 'project':
      return FolderGit2
    case 'global':
      return Globe
  }
})
</script>

<style scoped>
.scope-badge {
  display: inline-flex;
  align-items: center;
  gap: 5px;
  height: 22px;
  padding: 0 8px;
  border-radius: 5px;
  font-size: 11.5px;
  font-weight: 600;
  letter-spacing: 0.02em;
}

.scope-badge-icon {
  width: 12px;
  height: 12px;
}

/* Agent scope: brand soft background + brand color text */
.scope-badge--agent {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

/* Global scope: soft gray background + text-2 color */
.scope-badge--global {
  background: var(--forebrain-button-alt-bg); /* --soft equivalent */
  color: var(--forebrain-text-2);
}

/* Project scope: dark background + white text */
.scope-badge--project {
  background: var(--forebrain-text);
  color: #ffffff;
}
</style>
```

**验证**:
```bash
# 组件应该可以正常导入和使用
cd frontend && pnpm dev
# 浏览器访问任意已有标签的页面，检查样式
```

---

#### Step 1.2: 迁移已有页面到新组件

**需要修改的文件**（11 个已有标签的页面）：

1. `frontend/src/views/SettingsView.vue`
2. `frontend/src/views/ProjectsView.vue`
3. `frontend/src/views/RulesView.vue`
4. `frontend/src/views/SubagentsView.vue`
5. `frontend/src/views/SkillsView.vue`
6. `frontend/src/views/MemoriesView.vue`
7. `frontend/src/views/CronView.vue`
8. `frontend/src/views/ProvidersView.vue`
9. `frontend/src/views/WorkshopView.vue`
10. `frontend/src/components/project/ProjectOverview.vue`
11. 其他 project 子页面（ProjectRules, ProjectPerm, ProjectMcp 等）

**迁移模板**（以 SettingsView.vue 为例）：

```diff
<template>
  <div class="...">
    <header class="mb-5 flex items-end justify-between gap-4">
      <div>
-       <span class="scope-badge scope-badge--global">
-         {{ t('scope.global') }}
-       </span>
+       <ScopeBadge type="global" :label="t('scope.global')" />
        <h1 class="...">{{ t('settings.title') }}</h1>
        ...
      </div>
    </header>
  </div>
</template>

<script setup lang="ts">
+ import ScopeBadge from '@/components/common/ScopeBadge.vue'
  // ... 其他导入
</script>

<style scoped>
- .scope-badge {
-   display: inline-flex;
-   align-items: center;
-   border-radius: 9999px;
-   padding: 2px 10px;
-   font-size: 11px;
-   font-weight: 500;
- }
- .scope-badge--global {
-   background: #101828;
-   color: #ffffff;
- }
+ /* 删除所有 scope-badge 相关样式 */
</style>
```

**批量替换策略**：
1. 先迁移 1-2 个文件，验证无问题
2. 再批量迁移其余文件
3. 删除每个文件的 `<style scoped>` 中的 `.scope-badge` 样式

**验证**:
```bash
# 确保所有页面的标签正常显示
pnpm dev
# 逐个访问 11 个页面，检查标签样式和图标
```

---

#### Step 1.3: 为 3 个缺失页面添加标签

**需要添加标签的页面**：

1. **ChannelsView.vue** (通道) - `agent` 作用域
2. **PermissionsView.vue** (权限) - `agent` 作用域
3. **ToolsView.vue** (工具) - `agent` 作用域

**添加模板**（以 ChannelsView.vue 为例）：

```diff
<template>
  <div class="...">
    <header class="mb-5 flex items-end justify-between gap-4">
      <div>
+       <ScopeBadge type="agent" :label="t('scope.agentWithName', { name: activePrimaryId })" />
        <h1 class="text-[1.5rem] font-medium leading-tight text-[var(--forebrain-text)]">
          {{ t('channels.title') }}
        </h1>
        <p class="mt-1 text-[13px] leading-relaxed text-[var(--forebrain-text-2)]">
          {{ t('channels.description') }}
        </p>
      </div>
      ...
    </header>
  </div>
</template>

<script setup lang="ts">
+ import ScopeBadge from '@/components/common/ScopeBadge.vue'
+ import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
+ const { activeId: activePrimaryId } = usePrimaryAgents()
  // ... 其他代码
</script>
```

**国际化文本**（如需添加）：
```typescript
// frontend/src/locales/zh-CN.ts
scope: {
  agent: '主代理',
  agentWithName: '主代理 · {name}',
  project: '项目',
  global: '全局',
}
```

**验证**:
```bash
pnpm dev
# 访问 /channels, /permissions, /tools 三个页面
# 确认标签显示、颜色、图标正确
```

---

#### Step 1.4: 验证完整性

**检查清单**：

- [ ] 所有 14 个主要页面都有作用域标签（11 个已有 + 3 个新增）
- [ ] 标签颜色符合设计稿：
  - Agent: 品牌浅色背景 + 品牌色文字
  - Project: 黑色背景 + 白色文字
  - Global: 灰色背景 + 灰色文字
- [ ] 所有标签都有正确的图标：
  - Agent: `user-round`
  - Project: `folder-git-2`
  - Global: `globe`
- [ ] 样式属性完全符合设计稿（gap: 5px, height: 22px, border-radius: 5px 等）
- [ ] 无重复样式代码（所有旧的 `.scope-badge` 样式已删除）
- [ ] 组件可复用（新增页面可直接使用 `<ScopeBadge>`）

**视觉回归测试**：
```bash
# 使用 Playwright 截图对比（如有 e2e 测试）
cd frontend && pnpm e2e
```

---

## Phase 2: 视觉尺寸对齐

**目标**: 修复 Logo、品牌区、按钮、输入框的尺寸差异（发现 2.1-2.3, 4.1-4.3）  
**工作量**: 1-2 小时  
**风险**: LOW  

### 实施步骤

#### Step 2.1: 修正 Logo 尺寸

**文件**: `frontend/src/assets/main.css`

**定位行**：`main.css:276-277`

```diff
.forebrain-rail-mark {
- width: 26px;
- height: 26px;
+ width: 28px;
+ height: 28px;
  flex: none;
}
```

**同时确认** `App.vue` 中的 props 声明：
```vue
<BrandMark :size="28" class="forebrain-rail-mark" />
```
✓ 已正确传递 28，只需修改 CSS 覆盖

**验证**:
```bash
# 重启 dev server
pnpm dev
# 检查菜单栏 logo 尺寸：应该是 28×28px
```

---

#### Step 2.2: 修正品牌区间距

**文件**: `frontend/src/assets/main.css`

**定位行**：`main.css:269`

```diff
.forebrain-rail-brand {
  display: flex;
  align-items: center;
- gap: 9px;
+ gap: 10px;
  padding: 2px 4px 4px;
  cursor: pointer;
}
```

---

#### Step 2.3: 修正 Wordmark 字号

**文件**: `frontend/src/assets/main.css`

**定位行**：`main.css:292`

```diff
.forebrain-rail-wordmark {
- font-size: 16px;
+ font-size: 15px;
  line-height: 1.2;
}
```

---

#### Step 2.4: 调整按钮样式

**文件**: `frontend/src/assets/main.css`

**查找 `.forebrain-btn` 定义**（需要先找到具体行号）

```diff
.forebrain-btn {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
- padding: 6px 12px;
+ padding: 0 12px;
- border-radius: 12px;
+ border-radius: 6px;
  border: 1px solid var(--forebrain-divider-strong);
  background: var(--forebrain-bg);
  color: var(--forebrain-text);
- font-size: 13px;
+ font-size: 12.5px;
  font-weight: 500;
  cursor: pointer;
  white-space: nowrap;
}
```

---

#### Step 2.5: 调整输入框样式

**文件**: `frontend/src/assets/main.css`

**查找 `.forebrain-field` 定义**

```diff
.forebrain-field {
  height: 34px;
- border-radius: 10px;
+ border-radius: 6px;
- padding: 8px 10px;
+ padding: 0 10px;
  border: 1px solid var(--forebrain-divider-strong);
  font: inherit;
  font-size: 13px;
  color: var(--forebrain-text);
  background: var(--forebrain-input-bg);
  width: 100%;
  box-sizing: border-box;
}

textarea.forebrain-field {
  height: auto;
  padding: 8px 10px;
+ line-height: 1.55;
  resize: vertical;
}
```

---

#### Step 2.6: 验证完整性

**检查清单**：

- [ ] Logo 尺寸: 28×28px（在浏览器开发者工具中测量）
- [ ] 品牌区间距: logo 与 "Forebrain Harness" 之间 10px
- [ ] Wordmark 字号: 15px
- [ ] 按钮 border-radius: 6px
- [ ] 按钮 padding: 垂直 0，水平 12px
- [ ] 输入框 border-radius: 6px
- [ ] 输入框 padding: 单行垂直 0，水平 10px；多行垂直 8px

**视觉验证**：打开设计稿和实际页面对比截图

---

## Phase 3: 通用组件实现

**目标**: 实现缺失的 `.badge`、`.sw`、`.card` 组件（发现 4.4-4.6）  
**工作量**: 4-5 小时  
**风险**: LOW  

### 实施步骤

#### Step 3.1: 实现 Badge 徽标组件

**文件**: `frontend/src/components/common/BadgeComponent.vue`

```vue
<template>
  <span :class="['forebrain-badge', variantClass]">
    <slot />
  </span>
</template>

<script setup lang="ts">
import { computed } from 'vue'

type BadgeVariant = 'default' | 'gray' | 'success' | 'warning' | 'error'

interface Props {
  variant?: BadgeVariant
}

const props = withDefaults(defineProps<Props>(), {
  variant: 'default',
})

const variantClass = computed(() => {
  switch (props.variant) {
    case 'gray':
      return 'forebrain-badge--gray'
    case 'success':
      return 'forebrain-badge--success'
    case 'warning':
      return 'forebrain-badge--warning'
    case 'error':
      return 'forebrain-badge--error'
    default:
      return 'forebrain-badge--default'
  }
})
</script>

<style scoped>
.forebrain-badge {
  display: inline-flex;
  align-items: center;
  height: 20px;
  padding: 0 7px;
  border-radius: 999px;
  font-size: 11px;
  font-weight: 500;
  white-space: nowrap;
}

/* Default: brand soft */
.forebrain-badge--default {
  background: var(--forebrain-brand-soft);
  color: var(--forebrain-brand-1);
}

/* Gray */
.forebrain-badge--gray {
  background: var(--forebrain-button-alt-bg);
  color: var(--forebrain-text-2);
}

/* Success */
.forebrain-badge--success {
  background: var(--forebrain-success-soft);
  color: var(--forebrain-success);
}

/* Warning */
.forebrain-badge--warning {
  background: var(--forebrain-warning-soft);
  color: var(--forebrain-warning);
}

/* Error: white background with red border */
.forebrain-badge--error {
  background: var(--forebrain-bg);
  color: var(--forebrain-danger);
  border: 1px solid var(--forebrain-danger);
}
</style>
```

**使用示例**：
```vue
<BadgeComponent>默认</BadgeComponent>
<BadgeComponent variant="gray">灰色</BadgeComponent>
<BadgeComponent variant="success">成功</BadgeComponent>
<BadgeComponent variant="warning">警告</BadgeComponent>
<BadgeComponent variant="error">错误</BadgeComponent>
```

---

#### Step 3.2: 实现 Switch 开关组件

**文件**: `frontend/src/components/common/SwitchComponent.vue`

```vue
<template>
  <button
    type="button"
    role="switch"
    :aria-checked="modelValue"
    :class="['forebrain-switch', { 'forebrain-switch--on': modelValue }]"
    :disabled="disabled"
    @click="toggle"
  >
    <span class="forebrain-switch-thumb" aria-hidden="true" />
  </button>
</template>

<script setup lang="ts">
interface Props {
  modelValue: boolean
  disabled?: boolean
}

interface Emits {
  (e: 'update:modelValue', value: boolean): void
}

const props = withDefaults(defineProps<Props>(), {
  disabled: false,
})

const emit = defineEmits<Emits>()

function toggle() {
  if (!props.disabled) {
    emit('update:modelValue', !props.modelValue)
  }
}
</script>

<style scoped>
.forebrain-switch {
  width: 32px;
  height: 18px;
  border-radius: 999px;
  background: var(--forebrain-divider-strong);
  position: relative;
  flex: none;
  border: 0;
  cursor: pointer;
  padding: 0;
  transition: background 120ms ease;
}

.forebrain-switch:disabled {
  opacity: 0.5;
  cursor: not-allowed;
}

.forebrain-switch-thumb {
  position: absolute;
  top: 2px;
  left: 2px;
  width: 14px;
  height: 14px;
  border-radius: 50%;
  background: #ffffff;
  transition: left 120ms ease;
}

.forebrain-switch--on {
  background: var(--forebrain-brand-1);
}

.forebrain-switch--on .forebrain-switch-thumb {
  left: 16px;
}
</style>
```

**使用示例**：
```vue
<script setup>
import { ref } from 'vue'
const enabled = ref(false)
</script>

<template>
  <SwitchComponent v-model="enabled" />
</template>
```

---

#### Step 3.3: 实现 Card 卡片组件

**文件**: `frontend/src/components/common/CardComponent.vue`

```vue
<template>
  <div class="forebrain-card">
    <div v-if="$slots.header" class="forebrain-card-header">
      <slot name="header" />
    </div>
    <div class="forebrain-card-body">
      <slot />
    </div>
  </div>
</template>

<style scoped>
.forebrain-card {
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  padding: 16px;
  display: grid;
  gap: 12px;
  align-content: start;
  background: var(--forebrain-bg);
}

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

.forebrain-card-body {
  display: grid;
  gap: 12px;
}
</style>
```

**使用示例**：
```vue
<CardComponent>
  <template #header>
    <h3>卡片标题</h3>
    <button>操作</button>
  </template>
  <p>卡片内容</p>
</CardComponent>
```

---

#### Step 3.4: 创建组件演示页面

**文件**: `frontend/public/component-gallery.html`

```html
<!DOCTYPE html>
<html lang="zh-CN">
<head>
  <meta charset="UTF-8">
  <meta name="viewport" content="width=device-width, initial-scale=1.0">
  <title>组件库演示 - Forebrain Harness</title>
  <style>
    body {
      font-family: -apple-system, BlinkMacSystemFont, sans-serif;
      padding: 40px;
      background: #f7f8fa;
    }
    .demo-section {
      background: white;
      border-radius: 8px;
      padding: 24px;
      margin-bottom: 24px;
    }
    .demo-section h2 {
      margin-top: 0;
      border-bottom: 1px solid #e4e7ec;
      padding-bottom: 12px;
    }
    .demo-row {
      display: flex;
      gap: 12px;
      flex-wrap: wrap;
      margin: 16px 0;
    }
  </style>
</head>
<body>
  <h1>Forebrain Harness 组件库演示</h1>
  
  <div class="demo-section">
    <h2>Badge 徽标</h2>
    <div class="demo-row">
      <span class="forebrain-badge forebrain-badge--default">默认</span>
      <span class="forebrain-badge forebrain-badge--gray">灰色</span>
      <span class="forebrain-badge forebrain-badge--success">成功</span>
      <span class="forebrain-badge forebrain-badge--warning">警告</span>
      <span class="forebrain-badge forebrain-badge--error">错误</span>
    </div>
  </div>
  
  <div class="demo-section">
    <h2>Switch 开关</h2>
    <div class="demo-row">
      <button class="forebrain-switch" aria-checked="false">
        <span class="forebrain-switch-thumb"></span>
      </button>
      <button class="forebrain-switch forebrain-switch--on" aria-checked="true">
        <span class="forebrain-switch-thumb"></span>
      </button>
    </div>
  </div>
  
  <div class="demo-section">
    <h2>Scope Badge 作用域标签</h2>
    <div class="demo-row">
      <span class="scope-badge scope-badge--agent">
        <svg><!-- user-round icon --></svg>
        主代理 · main
      </span>
      <span class="scope-badge scope-badge--project">
        <svg><!-- folder-git-2 icon --></svg>
        项目
      </span>
      <span class="scope-badge scope-badge--global">
        <svg><!-- globe icon --></svg>
        全局
      </span>
    </div>
  </div>
  
  <!-- 加载实际样式 -->
  <link rel="stylesheet" href="/src/assets/main.css">
</body>
</html>
```

**访问**: `http://localhost:5173/component-gallery.html`

---

#### Step 3.5: 验证完整性

**检查清单**：

- [ ] Badge 组件：5 个变体都正常显示
- [ ] Switch 组件：开关切换动画流畅
- [ ] Card 组件：边框、圆角、内边距符合设计稿
- [ ] 组件演示页面可正常访问
- [ ] 所有组件都有 TypeScript 类型定义
- [ ] 所有组件都支持 aria 属性（无障碍）

---

## Phase 4: 技术债清理

**目标**: 清理重复代码，完善文档（发现 3.5）  
**工作量**: 1-2 小时  
**风险**: LOW  

### 实施步骤

#### Step 4.1: 验证旧样式已删除

**检查命令**：
```bash
# 确保没有遗留的 .scope-badge 局部样式
cd frontend
grep -r "\.scope-badge" src/views src/components --include="*.vue" | grep -v "ScopeBadge.vue"
# 应该返回空结果
```

如有遗留，逐个删除 `<style scoped>` 中的 `.scope-badge` 定义。

---

#### Step 4.2: 创建组件文档

**文件**: `frontend/docs/components.md`

```markdown
# 组件库文档

本文档记录 Forebrain Harness 前端的可复用组件。

## ScopeBadge 作用域标签

显示页面的数据作用域（主代理/项目/全局）。

**Props**:
- `type`: `'agent' | 'project' | 'global'` - 作用域类型
- `label`: `string` - 显示文本

**示例**:
```vue
<ScopeBadge type="agent" label="主代理 · main" />
<ScopeBadge type="project" label="项目" />
<ScopeBadge type="global" label="全局" />
```

**设计规范**:
- 高度: 22px
- 圆角: 5px
- 字号: 11.5px
- 字重: 600
- 图标: 12×12px

---

## BadgeComponent 徽标

显示状态、类别或数字标记。

**Props**:
- `variant`: `'default' | 'gray' | 'success' | 'warning' | 'error'` - 变体

**示例**:
```vue
<BadgeComponent>默认</BadgeComponent>
<BadgeComponent variant="success">已连接</BadgeComponent>
<BadgeComponent variant="error">失败</BadgeComponent>
```

**设计规范**:
- 高度: 20px
- 圆角: 999px (完全圆角)
- 字号: 11px
- 字重: 500

---

## SwitchComponent 开关

双态开关组件，支持 v-model。

**Props**:
- `modelValue`: `boolean` - 开关状态
- `disabled`: `boolean` - 是否禁用

**事件**:
- `update:modelValue` - 状态变化时触发

**示例**:
```vue
<script setup>
const enabled = ref(false)
</script>

<template>
  <SwitchComponent v-model="enabled" />
</template>
```

**设计规范**:
- 尺寸: 32×18px
- 圆点: 14×14px
- 激活时圆点位置: left 16px

---

## CardComponent 卡片

内容容器，支持 header slot。

**Slots**:
- `header` - 卡片头部（可选）
- `default` - 卡片主体内容

**示例**:
```vue
<CardComponent>
  <template #header>
    <h3>标题</h3>
  </template>
  <p>内容</p>
</CardComponent>
```

**设计规范**:
- 边框: 1px solid var(--forebrain-divider)
- 圆角: 8px
- 内边距: 16px
```

---

#### Step 4.3: 更新 CHANGELOG

**文件**: `CHANGELOG.md`（在对应版本下添加）

```markdown
### Fixed

- 修正菜单栏 Logo 尺寸为 28×28px，与设计稿一致
- 修正品牌区间距为 10px
- 修正 Wordmark 字号为 15px
- 修正按钮和输入框的 border-radius 为 6px
- 修正作用域标签颜色，符合设计稿规范

### Added

- 新增 `ScopeBadge` 作用域标签组件，支持图标显示
- 为 ChannelsView、PermissionsView、ToolsView 添加作用域标签
- 新增 `BadgeComponent` 徽标组件（5 个变体）
- 新增 `SwitchComponent` 开关组件
- 新增 `CardComponent` 卡片组件
- 新增组件库文档 `frontend/docs/components.md`
- 新增组件演示页面 `component-gallery.html`

### Changed

- 重构所有作用域标签使用统一组件，清理 80+ 行重复代码
- 调整按钮 padding 为垂直 0、水平 12px
- 调整输入框 padding 为垂直 0（单行）、水平 10px
```

---

## 完成标准（Done Criteria）

在声明任何 Phase 完成之前，必须满足以下条件：

### Phase 1 完成标准

- [ ] 所有 14 个主要页面都有作用域标签
- [ ] 标签颜色完全符合设计稿（用浏览器开发者工具验证 CSS 变量值）
- [ ] 所有标签都有正确的图标
- [ ] 无任何 `<style scoped>` 中遗留的 `.scope-badge` 定义
- [ ] ScopeBadge 组件有完整的 TypeScript 类型
- [ ] `pnpm build` 成功无错误

### Phase 2 完成标准

- [ ] Logo 尺寸测量为 28×28px（浏览器测量）
- [ ] 品牌区 gap 测量为 10px
- [ ] Wordmark 字号为 15px
- [ ] 按钮 border-radius 为 6px
- [ ] 输入框 border-radius 为 6px
- [ ] `pnpm build` 成功无错误

### Phase 3 完成标准

- [ ] Badge、Switch、Card 三个组件都可正常导入使用
- [ ] 组件演示页面可正常访问
- [ ] 所有组件都有 TypeScript 类型定义
- [ ] 所有组件都支持无障碍属性
- [ ] `pnpm build` 成功无错误

### Phase 4 完成标准

- [ ] `grep` 确认无遗留的旧样式代码
- [ ] `frontend/docs/components.md` 已创建并完整
- [ ] CHANGELOG.md 已更新
- [ ] `pnpm build` 成功无错误

---

## 回滚方案（Rollback Plan）

如任一 Phase 出现问题，按以下步骤回滚：

### 回滚 Phase 1

```bash
# 恢复所有修改的文件
git checkout eafaf46 -- frontend/src/views/ChannelsView.vue
git checkout eafaf46 -- frontend/src/views/PermissionsView.vue
git checkout eafaf46 -- frontend/src/views/ToolsView.vue
git checkout eafaf46 -- frontend/src/views/SettingsView.vue
# ... 其他修改的文件

# 删除新增的组件
rm frontend/src/components/common/ScopeBadge.vue

# 验证回滚
pnpm dev
```

### 回滚 Phase 2

```bash
# 恢复 main.css
git checkout eafaf46 -- frontend/src/assets/main.css

# 验证回滚
pnpm dev
```

### 回滚 Phase 3

```bash
# 删除新增的组件
rm frontend/src/components/common/BadgeComponent.vue
rm frontend/src/components/common/SwitchComponent.vue
rm frontend/src/components/common/CardComponent.vue
rm frontend/public/component-gallery.html

# 验证回滚
pnpm dev
```

### 回滚 Phase 4

```bash
# 删除新增的文档
rm frontend/docs/components.md

# 恢复 CHANGELOG
git checkout eafaf46 -- CHANGELOG.md

# 验证回滚
pnpm build
```

---

## 风险评估

| 风险 | 可能性 | 影响 | 缓解措施 |
|------|--------|------|----------|
| 组件迁移遗漏部分页面 | MEDIUM | HIGH | 使用 grep 验证所有使用点 |
| CSS 变量名错误导致样式失效 | LOW | HIGH | 先在 1 个页面验证，再批量迁移 |
| 新组件与现有组件冲突 | LOW | MEDIUM | 使用唯一的 `forebrain-` 前缀 |
| 尺寸调整影响布局 | LOW | LOW | 调整前截图对比 |

---

## 维护说明

### 添加新页面时

1. 使用 `<ScopeBadge>` 组件，不要手写 span 标签
2. 根据页面类型选择正确的 `type` 属性：
   - 主代理页面: `type="agent"`
   - 项目页面: `type="project"`
   - 全局设置: `type="global"`

### 修改作用域标签样式时

1. 只修改 `ScopeBadge.vue` 一个文件
2. 不要在其他文件的 `<style scoped>` 中覆盖样式
3. 修改后验证所有页面的标签

### 使用新组件时

1. 参考 `frontend/docs/components.md` 文档
2. 参考 `component-gallery.html` 演示页面
3. 保持组件 API 稳定，避免破坏性变更

---

## 相关资源

- **设计稿**: `/Users/doudou/Downloads/Forebrain Harness 全站预览.html`
- **基准 commit**: `eafaf46`
- **审核报告**: `docs/plan/FRONTEND_DESIGN_ALIGNMENT_PLAN.md`（本文档）
- **组件文档**: `frontend/docs/components.md`（Phase 4 生成）
- **演示页面**: `http://localhost:5173/component-gallery.html`（Phase 3 生成）

---

**计划状态**: 已实施（2026-10-08）  
**最后更新**: 2026-10-08  
**预计完成**: 8-12 小时分 4 个 Phase 执行

---

## 验收记录（2026-10-08）

**基准**：`eafaf46`（执行时 HEAD 未漂移）；改动全部留在工作区，未 commit。

### 各 Phase 结果

- **Phase 1（作用域标签）**：已实施。新增 `frontend/src/components/common/ScopeBadge.vue`；
  全部 20 处页头/组件标签改用 `<ScopeBadge>`（9 个 view + 8 个 project 子组件 + 新增 3 页
  ChannelsView / PermissionsView / ToolsView），并删除了 10 个文件里各自的 `.scope-badge`
  样式（含已变空的 `<style scoped>` 块）。
  - `grep -rn "\.scope-badge" frontend/src --include="*.vue" | grep -v ScopeBadge.vue` → 空。
  - 颜色经真机取值核对：agent `#EBF0F9`/`#1F4A9E`、project `#101828`/`#fff`、
    global `#F2F4F7`/`#475467`，与设计稿一致。
  - `data-testid="scope-badge"` 经 Vue 属性穿透落在组件根元素上（真机验证：元素为
    `SPAN | scope-badge scope-badge--project`），`e2e/tenant-shell.spec.ts` 的两处断言不受影响。
- **Phase 2（视觉尺寸）**：已实施。`.forebrain-rail-mark` 26→28px、`.forebrain-rail-brand`
  gap 9→10px、`.forebrain-rail-wordmark` 16→15px、`.forebrain-btn` 圆角 `0.75rem→0.375rem`
  + padding `0.375rem 0.75rem→0 0.75rem` + 字号 `0.8125rem→0.78125rem`、`.forebrain-field`
  圆角 10→6px + padding `8px 10px→0 10px`，并补 `textarea.forebrain-field`
  （`height:auto; padding:8px 10px; line-height:1.55; resize:vertical`）。
- **Phase 3（通用组件）**：已实施。新增 `BadgeComponent.vue`（5 变体）、`SwitchComponent.vue`
  （`v-model` + `role="switch"`）、`CardComponent.vue`（header slot），以及演示页
  `frontend/component-gallery.html` + `frontend/src/gallery/`。演示页直接挂载真实组件。
- **Phase 4（技术债）**：已实施。`grep` 确认无遗留旧样式；新增
  `frontend/docs/components.md`。

### 与计划的偏差（均已在执行时裁决）

1. **作用域标签位置不变**：计划 Step 1.2 的 diff 把标签放在 `<h1>` 之前，但实际代码一直是
   "h1 之后、同一 flex 行内"。**保留实际位置**（改动最小；owner 已裁决不引入设计稿的
   `.pg`/`.ph` 包裹结构）。新增的 3 页沿用同样布局，与 ProvidersView / CronView 一致。
2. **新增页标签文案复用 `t('scope.agent')`**：计划建议新增 `scope.agentWithName` 并接入
   `usePrimaryAgents`。为与其余 9 个 agent 页面一致且不新增 i18n 键/依赖，未采纳。
3. **`.forebrain-field` 未加 `height:34px` / `width:100%` / `box-sizing`**：全仓 56 处调用依赖
   Tailwind 的 `h-9`/`w-28`/`flex-1` 等工具类做尺寸覆盖，而 `.forebrain-field` 规则位置在
   `@tailwind utilities` 之后会覆盖这些工具类；加宽高会造成实际回归。计划 Step 2.6 的
   检查清单也只要求圆角与 padding，故按清单执行。
4. **未改 CHANGELOG.md**：本仓库 CHANGELOG 由 release-please 管理
   （`release-please-config.json` 的 `changelog-path`），手工在"对应版本下添加"会被下次
   release-please 运行覆盖，且与 FOREBRAIN.md 的发布规则冲突。条目将由 PR 的
   Conventional Commits 标题生成。
5. **演示页放在 `frontend/component-gallery.html`（不进构建）**：计划写的 `public/` 静态页
   无法引入 SFC 的 scoped 样式（真实组件样式不在 `main.css`），会渲染成无样式文本；改为
   仅开发服务器可访问的 Vue 挂载页，不改 vite 构建入口、不影响发布产物。
   访问：`http://localhost:3000/component-gallery.html`（dev 端口以 vite.config.ts 为准）。

### 已验证

```bash
cd frontend
pnpm build                 # ✓ built，无错误
pnpm exec vue-tsc --noEmit # ✓ 0 诊断
pnpm test                  # ✓ 40 files / 312 tests passed
```

真机（headless Chrome，Playwright，`channel: chrome`）渲染组件演示页：
Badge ×6、ScopeBadge ×3、Switch ×3（`aria-checked` = false/true/true）、Card ×1；
点击 Switch 后 `aria-checked` false→true；控制台/页面 0 error。

### 本期未处理（相邻问题，未纳入本计划）

> 本节原列的 5 条已按 owner 指令"相邻问题必须定位根因并彻底修复"处理，见下方
> **第二轮**；此处保留原文以便对照。

- **SkillsTable.vue:46** 的 `class="scope-badge"`：技能来源徽标（`SkillOrigin` 5 值、
  设计稿中**不带图标**），与页头作用域标签不是同一控件；该 class 目前无任何样式定义
  （一直是"死类名"）。未改动。
- 设计稿 `ph()` 页头把作用域标签单独成行放在标题上方，产品现有实现是行内紧跟标题；
  二者差异未被审核报告列为问题，本期未统一。
- 三个新组件尚无产品内调用点（计划只要求"设计系统完整性 + 演示页 + 文档"；现有开关是
  checkbox、卡片是 `rounded-2xl` + `--forebrain-surface`，直接替换会带来计划未要求的
  视觉/交互变化）。
- `PendingActionsPanel.test.ts` 存在**既有、与本期无关的偶发** teardown 竞态
  （`vue-stream-markdown` 的懒加载 import 在环境销毁后 resolve，vitest 报
  `EnvironmentTeardownError`，`pnpm test` 偶发 exit 1；重跑通过）。该测试的模块图不包含
  本期改动的任何文件。
- 执行 `pnpm build` 会写入已提交的 `pkg/gateway/dist`（embed 产物），工作区因此带有大量
  dist 资源增删；这是计划"pnpm build 成功"验收动作的固有副作用。

---

## 第二轮：相邻问题根因修复（2026-10-08）

owner 指令："相邻问题必须定位根因并彻底修复"。第一轮列为"未处理/相邻"的条目逐条查根因。

### 1. `SkillsTable` 的技能来源徽标是死类名（真缺陷）

- **根因**：`SkillsTable.vue` 全文没有 `<style>` 块；`.scope-badge` 的外观历来只写在各个
  页面自己的 `<style scoped>` 里（无全局规则）→ 该徽标自始至终以纯文本渲染。
- **修（按设计稿）**：设计稿 `skillRow`（第 540 行）里来源列是
  `'<span class="badge gray">' + src` → 改用 `<BadgeComponent variant="gray">`；同一行的
  启用/停用标记在设计稿第 580 行是 `.badge ok|gray` → 改用
  `<BadgeComponent :variant="row.enabled ? 'success' : 'gray'">`。
- 顺带删掉无人引用的 `data-skill-origin-badge`（与行上的 `data-skill-origin` 重复，
  仓库内无任何消费者）。

### 2. 行内手搓开关 → `SwitchComponent`（同类缺陷：产品重复实现设计稿的 `.sw`）

- **根因**：`SwitchComponent` 无调用点，是因为 `SkillsTable` 用
  `<input type=checkbox class="peer sr-only">` + 两个 `peer-*` span 手搓了一份开关，
  尺寸 20×36px 与设计稿 `.sw` 的 32×18px 不符。
- **修**：改用 `SwitchComponent`（设计稿 `skillRow` 第 540 行就是 `.sw`）。契约保持：
  `role="switch"` + `aria-checked` 能被 Playwright `toBeChecked()` 读到（已单独实测）；
  两个测试钩子合并成一个 `data-testid="skill-toggle-<name>"`，`e2e/skills-lifecycle.spec.ts`
  原先点击的 `skill-switch-*` 随之改为同一元素（**断言本身不变**，只改选择器与注释）。

### 3. 同类穷举：设计稿明确用 `.sw` 的其余开关

- `ChannelsView` 通道启用：设计稿第 801 行 `ch()` 用 `.sw` → 改 `SwitchComponent`
  （仍包在 `<label>` 里；已实测 label 会把文本点击转发给内部 button，可访问名与点击热区不变）。
- `MemorySwitchesTab` 三个记忆开关：设计稿第 880 行同一组行用 `.sw` → 改 `SwitchComponent` ×3。
- **未改（不是同类）**：`ScheduleBuilder` 的星期多选、`SkillsTable` 的批量选择、项目表单里的
  布尔字段——勾选/多选与表单字段，设计稿中没有对应的 `.sw`。

### 4. `pnpm test` 偶发 exit 1（既有失败）→ 根因 + 修

- **现象**：40 files / 312 tests 全绿，但整轮偶尔 exit 1，报
  `EnvironmentTeardownError: Cannot load '…/modal-*.js' imported from '…/image-C*.js'
  after the environment was torn down`。
- **根因链**：`vue-stream-markdown/dist/index.js` 用
  `defineAsyncComponent(() => import("./*.js"))` 注册全部节点渲染器（37 个 chunk）；
  `PendingActionsPanel.test.ts` 渲染计划正文时命中 `image` 渲染器、它正在加载
  `modal-*.js`；测试文件结束后 vitest 销毁 jsdom 环境，这个没人 await 的 import 随即
  reject，被计为 unhandled error（**没有任何测试失败**，红的只是退出码）。
- **修**：在该测试文件 `afterAll` 里 `await vi.dynamicImportSettled()`（vitest 4.1.4 自带
  API，正是为这个窗口设计的），让 import 在环境还活着时落地；**不改任何断言**。
- **证据**：修复前单文件 3 次跑 2 次报错；修复后单文件 5/5、全量 `pnpm test` 连跑 3 次
  0 unhandled、exit 0。

### 5. 死类名穷举（找同类）

- **做法**：脚本扫描 `frontend/src` 全部 462 个 class token，减去 css/scoped 样式里定义过的、
  再减去 Tailwind 能生成的 → 15 个候选，逐个核对"有设计稿意图吗 / 有消费者吗"。
- **结论**：唯一"有意图但缺样式"的仍是第 1 条的 `scope-badge`（已修）。
  - 测试钩子（单测/e2e 依赖，保留）：`chat-shell`、`diff-code`、`slash-group`。
  - 扫描器假阳性（`:class` 表达式里的比较字面量）：`completed`、`skipped`、`project`、
    `project_only`、`failed`、`needs-auth`。
  - 无样式意图的遗留钩子名（不改，仅记录）：`brand-mark`、`diff-view`、
    `forebrain-subagent-call-card`、`workspace-row--tabbed`。
- 顺带清掉本轮自己引入的死类名 `scope-badge-text`（`ScopeBadge.vue` 里没有任何规则）。

### 6. 验收工具链 bug：`scripts/acceptance/web_e2e.sh` 最后一步恒失败（既有）

- **根因**：第 10 步断言 `gateway status` 输出含 `status=200`，但该命令早已改成句子格式
  （`pkg/gateway/http_server_test.go` 钉的是 `Health     ok (HTTP 200)`）；HEAD 与工作区都
  没有 `status=200` → 这一步永远 `die`，`web e2e: PASS` 永远不会打印（掩盖真实结果）。
- **修**：断言改为 `ok (HTTP 200)`（与契约测试一致），并说明来源。
- **证据**：`FOREBRAIN_E2E_SPEC="e2e/tenant-shell.spec.ts e2e/memory-files.spec.ts
  e2e/project-space.spec.ts" scripts/acceptance/web_e2e.sh` → 21 passed + `web e2e: PASS`
  （exit 0）。此前同一命令 exit 1。

### 7. 全量 web e2e 结果与两个既有失败（已证与本轮改动无关）

`scripts/acceptance/web_e2e.sh`（全量，8.3 分钟）：**87 passed / 2 failed / 2 skipped**。

两个失败：

| 用例 | 现象 | 根因判定 |
|---|---|---|
| `e2e/banner.spec.ts:9` | 启动 banner 里没有 `Routes (` | 工作区在途改动删掉了路由表：`git diff pkg/gateway/http_server.go` 删除 `RouteInfo`/`Routes()` 与 `AddRoute` 的记录；而 **HEAD 有**（`git show HEAD:pkg/gateway/http_server.go \| grep -c RouteInfo` = 6），且计划 002（`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/README.md:74`）状态 **DONE**、明确要求"banner+104 路由+渠道路由"。→ 在途改动回归了已完成计划的行为，spec 本身是对的 |
| `e2e/exit-plan-approval.spec.ts:169` | 评审跑完（侧栏 `Plan review · <0.1s`）但卡片始终没有 `评审 ·`，180s 超时 | 该 spec、`PendingActionsPanel.vue`、`pkg/run`、`pkg/turn` 均未改动 → 疑似 fake provider 下评审 <0.1s 完成、回流路径的既有竞态；本轮未定位到 file:line 根因 |

**归属证明**：把我的全部 frontend 改动临时还原到 HEAD（`git checkout --` 25 个文件，
其余在途改动不动）后重跑同样两条 → **同样两条失败、同样耗时**
（`/tmp/we2e-baseline.log`）；随后已用备份 `rsync` 原样恢复（`git diff --stat -- frontend`
= 25 files / +100 / −193，与还原前一致）。据此判定两条既有失败与本轮改动无关。

### 8. owner 裁决结果（2026-10-08 回复）

1. **`CardComponent` 落地点 → "先给计划再实施"**：已写
   `docs/plan/FRONTEND_CARD_COMPONENT_ADOPTION_PLAN.md`（17 处 in-scope、逐处落地方式、
   行为变化矩阵、out-of-scope 清单、真机验收步骤），**待审批后实施**，本计划不再动这块。
2. **banner 少路由表 → owner："是我的要求"**（有意移除）。据此把过期的 spec 修掉：
   `frontend/e2e/banner.spec.ts` 不再断言 `Routes (` / 各路由行，改为断言当前 banner 的
   真实内容（logo、服务名、`Web UI`、`Auth`、`Listening on`、不泄漏 token、无 logfmt 噪声）。
   ⚠️ 记录不一致：计划 002（`docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/README.md:74`）状态
   仍是 **DONE** 且写着"banner+104 路由"，与 owner 现在的裁决冲突——**未擅自改他的计划树**，
   请 owner 自行更新状态行。
3. **评审结果回不到卡片 → owner 裁决：需求变更，测试断言按此语义修**（2026-10-08）。
   证据链：`git log -S deliverCompletedPlanReview -- pkg/tui` → `b7eda22 feat(turn): auto-deliver
   finished plan reviews to the planner`（2026-10-08，HEAD 祖先）；`pkg/tui/chat_surface.go:915-921`
   的现行注释即新契约——"A review that finished delivers itself: the planner revises against it
   and submits a fresh exit_plan_mode, so the approval the user answers next has already absorbed
   the review."；`git log b7eda22..HEAD -- frontend/e2e/exit-plan-approval.spec.ts` 为空 → spec
   自那次需求变更起从未更新。

   **已修（只改断言，未动任何生产代码）**：`frontend/e2e/exit-plan-approval.spec.ts`
   - 用例 `asking for a review runs it live and the answer comes back on the card` →
     `asking for a review hands it to the planner: the card closes and the timeline says so`。
     断言由"卡片上出现 `评审 ·` + 可再批准"改为"回传行出现（`[data-approval-id=
     "act-e2e-exitplan-live"][data-approval-status="denied"]` 内含 `Plan review delivered`
     / `revising the plan`）且原审批卡 count 0"——与 TUI 的既有断言同一语义
     （`pkg/tui/chat_session_test.go:12605` 的 `TestPlanReviewAutoDeliversToPlannerAfterDone`：
     action=denied(marker) → 恰好一条 `approval_resolved`（Reason=marker、Confirmation=回传句）
     → 门不再重提示 → 一行 "Plan review delivered" → 恢复输入含评审正文与指令头）。
   - 被移除的尾部（`exit-plan-approve-clear` + 线上 `clear_context: true` + 卡片消失）是**未变更的
     行为**，单独立为新用例 `approving with the context cleared says so on the wire and closes
     the card`（不请评审直接批准），避免丢覆盖。
   - 反证：把断言改回旧语义（`评审 ·` 可见）实测 **FAIL**（`element(s) not found`，30s），
     新断言 PASS——确认旧语义已不存在，改动不是"把红的改名成绿的"。
   - 验证：`FOREBRAIN_E2E_SPEC=e2e/exit-plan-approval.spec.ts scripts/acceptance/web_e2e.sh`
     → 4 passed + `web e2e: PASS`；`go test ./pkg/tui ./pkg/gateway ./pkg/turn -run PlanReview`
     → 3 包 ok；前端单测 `ApprovalCard/useChatStream/PendingActionsPanel` → 104 passed。
