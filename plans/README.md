# Forebrain Harness 改进计划索引

本目录包含由 `improve` skill 生成的代码改进计划。每个计划都是自包含的实施指南，可由其他执行器模型独立完成。

## 最新审核信息

- **审核日期**：2026-10-08
- **基准 commit**：eafaf46
- **审核类型**：TUI message queue bug 根因分析与修复
- **问题报告**：用户报告"入队消息等待一段时间后，按 Shift+← 无法撤回"

## 计划列表

| # | 标题 | 类别 | 状态 | 优先级 | 工作量 | 风险 |
|---|------|------|------|--------|--------|------|
| [001-fix-message-recall-after-detach](001-fix-message-recall-after-detach.md) | 修复 runtime detach 后消息无法撤回 | Bug Fix | TODO | **HIGH** | S | LOW |
| [001-frontend-component-design-spec-alignment](001-frontend-component-design-spec-alignment.md) | 前端通用组件样式规范对齐 | Design System | TODO | HIGH | M (4-6h) | MEDIUM |

## 执行顺序

推荐按以下顺序执行：

```
001-fix-message-recall-after-detach (独立，无依赖，优先修复)
001-frontend-component-design-spec-alignment (独立，需用户确认命名策略)
```

## 状态说明

- **TODO**：待执行
- **IN_PROGRESS**：执行中
- **BLOCKED**：被阻塞（等待决策或依赖）
- **DONE**：已完成
- **REJECTED**：已拒绝（说明原因）

---

## 最新审核发现总结（2026-10-08）

### 类别：Correctness / Message Queue Bug

**严重程度**：**HIGH**

**症状**：用户提交消息后，等待一段时间，按 Shift+← 键无法撤回该消息。

**根因**：

`pkg/run/turn_input.go` 中的 `InputQueue.Recall()` 方法错误地将 `q.rt == nil`（runtime detached）当作"不可撤回"的条件。实际语义是：

- `q.rt == nil`：runtime 已 detach，但消息仍在 `InputQueue.steers` 中（未交付）
- 真正的"已交付"标志：消息从 `steers` 移到 `delivered`（通过 `BeginSteerDelivery` → `Commit` 路径）

**错误逻辑**：
```go
case laneSteer:
    if q.rt == nil {           // ← 错误假设
        skipSteers = true
        continue
    }
```

**正确语义**：
- Runtime attached → 需要先从 `TurnInputRuntime.pending` 撤回
- Runtime detached → 消息只在 `InputQueue.steers` 中，可以直接撤回
- 已交付的消息 → 在 `InputQueue.delivered` 中，`newestLocked` 不会选中

**影响**：

- 用户体验严重受损：无法撤回刚提交的消息
- 复现条件：提交消息后等待 1-2 秒（让 turn 执行到 runtime detach 时刻）

**修复方案**：

见 `001-fix-message-recall-after-detach.md`，核心是放宽 `Recall()` 的撤回条件：
```go
case laneSteer:
    if q.rt != nil {
        if _, ok := q.rt.RetractLastSteer(); !ok {
            skipSteers = true
            continue
        }
    }
    // Runtime nil or retract succeeded → recall from queue
    laneEntries = &q.steers
```

**验证**：
- 单元测试：`TestRecallSteerAfterRuntimeDetach`
- TUI 真机：提交 → 等待 → Shift+← 撤回

---

## 历史审核记录

### 2026-10-08 - 设计稿与实现一致性审核（通用组件样式）

**审核范围**：`frontend/src/assets/main.css` vs `docs/plan/WEB_REDESIGN_AND_GATEWAY_ACCESS/app-preview.html`

**发现**：设计稿定义的通用组件规范（`.btn`, `.field`, `.badge`, `.sw`, `.card`）与 `main.css` 实现存在三类问题：

1. **命名空间不一致**：设计稿用简短类名（`.btn`），实现用前缀类名（`.forebrain-btn`）
2. **数值偏差**：按钮、输入框的 `border-radius` 和 `padding` 不匹配
3. **组件缺失**：`.badge`、`.sw`、`.card` 及部分变体完全缺失

**计划**：见 `001-frontend-component-design-spec-alignment.md`（需用户确认命名策略）

---

## 已审核但未计划的发现

无。

## 已考虑并拒绝的发现

无。

## 下一步

1. **优先执行**：`001-fix-message-recall-after-detach`（用户阻塞 bug）
2. **用户审阅**：`001-frontend-component-design-spec-alignment`，确认命名策略
3. **后续审核方向**（可选）：
   - 审核其他 TUI 交互缺陷
   - 审核 gateway 响应式布局断点
   - 审核暗色模式适配完整性

## 联系与反馈

如对计划有疑问或需要修订，请在计划文件对应章节添加注释，或联系计划作者。

---

**索引生成时间**：2026-10-08  
**Improve Skill 版本**：按 improve skill 模板生成
