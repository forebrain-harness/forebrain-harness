<template>
  <!-- The shell owns the viewport height; the thread just fills what is left
       of it, so the composer stays pinned without guessing the chrome's size. -->
  <div class="chat-shell flex h-full min-h-0 w-full flex-1 overflow-hidden">
    <div class="relative flex min-h-0 min-w-0 flex-1 flex-row overflow-hidden bg-[var(--forebrain-surface)]">
      <main class="mx-auto flex h-full min-h-0 w-full max-w-[880px] flex-1 flex-col overflow-hidden px-3 sm:px-5">
        <AgentViewTabs
          :records="agentTabRecords"
          :active="activeAgentView"
          :seen="seenAgentActivity"
          @select="openAgentView"
        />
        <div class="flex items-center justify-between gap-3 px-3 pt-3 sm:px-5">
          <div class="flex min-w-0 items-center gap-2">
            <span
              class="truncate text-sm font-medium text-[var(--forebrain-text)]"
              data-testid="chat-session-title"
            >{{ sessionTitle }}</span>
            <!-- A project's conversation says whose it is and leads back to
                 that project's space, where its sessions are listed. -->
            <RouterLink
              v-if="sessionProject"
              :to="`/projects/${encodeURIComponent(sessionProject.id)}/overview`"
              class="chat-project-tag"
              data-testid="chat-project-tag"
            >{{ t('chat.projectTag', { name: sessionProject.name }) }}</RouterLink>
          </div>
          <button
            type="button"
            class="inline-flex h-7 items-center gap-1.5 rounded-lg border border-[var(--forebrain-divider)] px-2.5 text-xs text-[var(--forebrain-text-2)] hover:bg-[var(--forebrain-button-alt-bg)]"
            :aria-pressed="workbench.open.value"
            data-testid="workbench-toggle"
            @click="workbench.toggle()"
          >
            <PanelRight class="size-3.5" aria-hidden="true" />
            {{ t('chat.workbench') }}
            <span
              v-if="runningRoster.length"
              class="inline-flex min-w-4 items-center justify-center rounded-full px-1 text-[10px] leading-4 text-[var(--forebrain-on-brand)]"
              style="background: var(--forebrain-brand-1)"
            >{{ runningRoster.length }}</span>
          </button>
        </div>
        <div class="min-h-0 flex-1 overflow-hidden">
          <Conversation
            ref="conversationRef"
            class="chat-scrollbar relative h-full overflow-y-scroll overscroll-contain"
            @scroll.passive="recordCurrentBrowseState"
          >
            <ConversationContent>
            <!-- Viewing a subagent replaces the thread rather than nesting in
                 it: its work belongs to that agent, not to the conversation the
                 user is having. -->
            <Transition name="forebrain-agent-view">
              <div v-if="activeSubagent" :key="activeSubagent.agentId" class="space-y-4">
                <!-- /context in this view, drawn with the conversation's own
                     panel so the two reports are read the same way. -->
                <ContextDebugPanel
                  v-if="subagentContextTarget === activeSubagent.agentId"
                  :data="subagentContextDebug"
                  :loading="subagentContextLoading"
                  :error="null"
                  @refresh="openSubagentContext(activeSubagent.agentId)"
                />
                <SubagentConversation
                  :record="activeSubagent"
                  :fold-state="browseStateFor(activeSubagent.agentId).folds"
                  :call-tasks="subagentCallTasks"
                  :now="subagentNow"
                  @back="openAgentView('')"
                  @open-agent="openAgentView"
                  @fold-change="updateAgentFold(activeSubagent.agentId, $event)"
                />
              </div>
            </Transition>
              <!-- Keep the primary DOM mounted while an agent view is open.
                   Its collapsibles therefore return exactly as the user left
                   them, while each agent's explicit state survives remounts. -->
              <div v-show="!activeSubagent" key="primary-view">
            <Alert v-if="historyError" variant="destructive" class="mb-4">
              <CircleAlert class="size-4" />
              <AlertTitle>{{ t('chat.historyLoadFailed') }}</AlertTitle>
              <AlertDescription class="space-y-2">
                <div class="whitespace-pre-line">{{ historyError }}</div>
                <button
                  type="button"
                  class="forebrain-btn forebrain-btn-ghost text-xs"
                  :disabled="historyLoading || !sessionId"
                  @click="sessionId && loadMessages(sessionId)"
                >{{ t('chat.retry') }}</button>
              </AlertDescription>
            </Alert>
            <ConversationEmptyState v-if="!messages.length && !isStreaming" :title="t('chat.emptyTitle')">
              <template #icon>
                <MessageSquare class="size-12 text-muted-foreground" />
              </template>
            </ConversationEmptyState>
            <template v-else>
              <div class="space-y-6">
                <template v-for="(msg, idx) in messages" :key="msg.id ?? idx">
                  <!-- A slash command's reply: UI output only, never part of the
                       transcript, so it is a notice card, not a message. -->
                  <div
                    v-if="msg.role === 'notice'"
                    class="mx-auto max-w-3xl rounded-lg border border-border bg-card px-4 py-3 text-sm text-card-foreground whitespace-pre-wrap break-words"
                  ><template v-if="msg.content">{{ msg.content }}</template><SlashPickerCard
                      v-if="msg.picker"
                      :class="msg.content ? 'mt-3 whitespace-normal' : 'whitespace-normal'"
                      :picker="msg.picker"
                      :picked="msg.picked"
                      @choose="(value) => pickFromNotice(msg, value)"
                    /></div>
                  <!-- v-if outside the loop: the role does not change per plan
                       block, and Vue gives v-if priority over v-for on the same
                       element, which reads as if it did. -->
                  <template v-if="msg.role === 'assistant'">
                  <template v-for="(block, planIdx) in getPlanBlocks(msg)" :key="planIdx">
                    <div class="space-y-2" :data-plan-block="`${msg.id ?? idx}-${planIdx}`">
                      <div class="flex items-center gap-2 text-sm font-medium text-[var(--forebrain-text)]">
                        <CheckCircle class="size-4 text-[var(--forebrain-brand-1)]" />
                        {{ t('chat.progress') }}
                      </div>
                      <div class="text-sm text-muted-foreground mb-2">{{ t('chat.taskPlan') }}</div>
                      <Plan :default-open="true">
                        <PlanHeader>
                          <div>
                            <PlanTitle>{{ t('chat.executionPlan') }}</PlanTitle>
                            <p class="text-sm text-muted-foreground mt-1">{{ block.plan.question }}</p>
                          </div>
                          <PlanTrigger />
                        </PlanHeader>
                        <PlanContent>
                          <div class="space-y-2">
                            <Task v-for="s in block.plan.steps" :key="s.stepId" :default-open="s.stepId === '1'">
                              <TaskTrigger :title="`${s.stepId}. ${s.description}`" />
                              <TaskContent>
                                <template v-if="Array.isArray(s.parameters?.query) && s.parameters.query.length">
                                  <TaskItem v-for="(q, qi) in s.parameters.query" :key="`qp-${qi}`"
                                    :class="qi < s.parameters.query.length - 1 ? 'line-through text-muted-foreground' : undefined">
                                    {{ q }}
                                  </TaskItem>
                                </template>
                                <TaskItem v-else>{{ s.description }}</TaskItem>
                                <TaskItem v-if="(s.estimatedTime ?? 0) > 0">{{ t('chat.estimate') }}: {{ s.estimatedTime }}s</TaskItem>
                              </TaskContent>
                            </Task>
                          </div>
                        </PlanContent>
                      </Plan>
                    </div>
                  </template>
                  </template>
                  <Message v-if="msg.role !== 'notice' && messageHasBubble(msg)" :from="messageBubbleRole(msg.role)" :class="messageClass(msg.role)">
                    <Avatar v-if="msg.role === 'assistant'" class="size-8 shrink-0 ring-1 ring-border">
                      <AvatarFallback class="bg-muted">
                        <Bot class="size-4 text-muted-foreground" />
                      </AvatarFallback>
                    </Avatar>
                    <MessageContent :class="messageContentClass(msg.role)">
                      <div
                        v-if="msg.role === 'assistant' && msg.planUpdates?.length"
                        class="mb-3 space-y-3"
                      >
                        <PlanUpdateCard
                          v-for="(pu, puIdx) in msg.planUpdates"
                          :key="`${msg.id ?? idx}-plan-update-${puIdx}`"
                          :plan="pu"
                        />
                      </div>
                      <div v-if="msg.role === 'assistant' && isStreaming && !msg.blocks?.length && !String(msg.content ?? '').trim()"
                        class="inline-flex items-center rounded-xl bg-muted px-4 py-3">
                        <span class="sr-only">{{ t('chat.thinking') }}</span>
                        <span class="typing-dots" aria-hidden="true">
                          <span class="typing-dot" />
                          <span class="typing-dot" />
                          <span class="typing-dot" />
                        </span>
                      </div>
                      <!-- What the turn said and did, in the order it happened:
                           prose, thinking, the calls it made and the gates it was
                           stopped at. A live turn and a reloaded one build this
                           same list, so a refresh shows the conversation the user
                           was just watching. -->
                      <template v-else-if="msg.blocks?.length">
                        <template v-for="(block, blockIdx) in msg.blocks" :key="`${msg.id ?? idx}-block-${blockIdx}`">
                          <div
                            v-if="block.kind === 'thinking'"
                            class="mb-2 whitespace-pre-wrap break-words overflow-visible text-[13px] leading-relaxed text-[var(--forebrain-muted-text)]"
                          >
                            {{ reasoningDisplayText(block.text) }}
                          </div>
                          <MessageResponse
                            v-else-if="block.kind === 'assistant'"
                            :key="`${msg.id ?? idx}-said-${blockIdx}`"
                            :content="block.text"
                          />
                          <ToolCallCard
                            v-else-if="block.kind === 'tool' && !block.step.subagentCall"
                            class="my-2"
                            :step="block.step"
                            :default-open="false"
                          />
                          <!-- A subagent_* call is one card: the call's own
                               facts and the tasks it dispatched, checked,
                               waited for or stopped — its prompt and its
                               result JSON stay out of the conversation. -->
                          <SubagentCallCard
                            v-else-if="block.kind === 'tool'"
                            class="my-2"
                            :step="block.step"
                            :live="subagentCallTasks.get(block.step.stepId)"
                            :now="subagentNow"
                            @open="openAgentView"
                          />
                          <!-- An execution dispatched with no call of its own
                               — a plan review, a runtime dispatch — is the
                               same card, keyed by its execution. -->
                          <SubagentCallCard
                            v-else-if="block.kind === 'subagent'"
                            class="my-2"
                            :step="block.step"
                            :live="subagentCallTasks.get(block.stepId)"
                            :now="subagentNow"
                            @open="openAgentView"
                          />
                          <ApprovalCard
                            v-else-if="block.kind === 'approval'"
                            class="my-2"
                            :block="block"
                          />
                          <CompactionCard
                            v-else-if="block.kind === 'compaction'"
                            class="my-2"
                            :data="block.compaction"
                          />
                          <GoalLine
                            v-else-if="block.kind === 'goal'"
                            class="my-2"
                            :goal="block.goal"
                            @open-check="openAgentView"
                          />
                          <RunErrorBlock
                            v-else-if="block.kind === 'error'"
                            class="my-2"
                            :text="block.text"
                            :detail="block.detail"
                          />
                        </template>
                      </template>
                      <!-- What the user sent reads exactly as they wrote it, the way
                           the terminal shows it: never reinterpreted as Markdown.
                           A message the runtime sent on their behalf says who
                           sent it, above the words. -->
                      <div v-else-if="msg.role === 'user' && String(msg.content ?? '').trim()">
                        <p v-if="originLabel(msg.origin)" data-testid="message-origin"
                          class="mb-1 text-[11px] font-medium text-[var(--forebrain-muted-text)]">{{ originLabel(msg.origin) }}</p>
                        <div class="whitespace-pre-wrap break-words">{{ msg.content }}</div>
                      </div>
                      <MessageResponse v-else-if="String(msg.content ?? '').trim()" :key="`msg-content-${msg.id ?? idx}`" :content="msg.content" />
                      <div
                        v-if="msg.role === 'assistant' && msg.turnDiffs?.length"
                        class="mt-3 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-3"
                      >
                        <DiffView
                          :files="msg.turnDiffs"
                          :summary="msg.turnDiffs[0]?.summary"
                          :total-lines="msg.turnDiffs.reduce((s, d) => s + (d.added ?? 0) + (d.deleted ?? 0), 0)"
                        />
                      </div>
                      <RunWorkedLine :line="workedLineOf(msg, t)" />
                      <ul v-if="msg.role === 'user' && msg.attachments?.length" class="mt-2 flex flex-wrap gap-2"
                        :aria-label="t('chat.messageAttachments')">
                        <li v-for="(attachment, index) in msg.attachments" :key="attachment.fileId ?? attachment.path ?? index">
                          <component :is="attachment.fileId ? 'a' : 'span'"
                            v-bind="attachment.fileId ? { href: attachmentUrl(attachment.fileId), target: '_blank', rel: 'noopener noreferrer' } : {}"
                            class="inline-flex min-h-7 max-w-[260px] items-center gap-1.5 rounded-md border border-border bg-background px-2 py-1 text-xs font-medium text-foreground"
                            :class="attachment.fileId ? 'group hover:bg-accent hover:text-accent-foreground' : ''"
                            :title="attachment.path ?? attachment.name">
                            <ImageIcon v-if="isImageAttachment(attachment)" class="size-3.5 shrink-0 text-muted-foreground group-hover:text-accent-foreground/80" aria-hidden="true" />
                            <Paperclip v-else class="size-3.5 shrink-0 text-muted-foreground group-hover:text-accent-foreground/80" aria-hidden="true" />
                            <span class="min-w-0 break-all">{{ attachment.name || t('chat.attachment') }}</span>
                          </component>
                        </li>
                      </ul>
                    </MessageContent>
                    <Avatar v-if="msg.role === 'user'" class="size-8 shrink-0 ring-1 ring-border">
                      <AvatarFallback class="bg-muted">
                        <User class="size-4 text-muted-foreground" />
                      </AvatarFallback>
                    </Avatar>
                  </Message>
                </template>
              </div>
              <Alert v-if="error" variant="destructive" class="mt-2">
                <CircleAlert class="size-4" />
                <AlertTitle>{{ t('chat.requestFailed') }}</AlertTitle>
                <AlertDescription class="whitespace-pre-line">{{ error }}</AlertDescription>
              </Alert>
            </template>
              </div>
            </ConversationContent>
            <ConversationScrollButton />
          </Conversation>
        </div>

        <details
          v-if="sessionId"
          data-testid="session-workspace"
          class="mx-auto mb-2 w-full max-w-[880px] shrink-0 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-2 text-sm text-[var(--forebrain-text-2)] sm:px-5"
        >
          <summary class="cursor-pointer select-none font-medium text-[var(--forebrain-text)]">
            <span class="inline-flex w-full flex-wrap items-center gap-2 pr-2">
              <span>{{ t('chat.sessionWorkspace') }}</span>
              <span
                class="inline-flex items-center rounded-full border px-2 py-0.5 text-[10px] font-semibold uppercase tracking-[0.14em]"
                :class="sessionContextSummary.signalClass"
              >
                {{ sessionContextSummary.signalLabel }}
              </span>
              <span class="text-[11px] font-normal text-[var(--forebrain-muted-text)]">
                {{ sessionContextSummary.line }}
              </span>
            </span>
          </summary>
          <div class="mt-3 max-h-[40vh] space-y-4 overflow-y-auto text-[13px] leading-snug">
            <div v-if="sessionTodosList.length" class="space-y-1">
              <div class="text-xs font-medium uppercase tracking-wide text-[var(--forebrain-muted-text)]">{{ t('chat.todos') }}</div>
              <ul class="list-disc space-y-1 pl-4">
                <li v-for="t in sessionTodosList" :key="t.id">
                  <span class="font-mono text-[11px] text-[var(--forebrain-muted-text)]">{{ t.status }}</span>
                  {{ t.id }}：{{ t.content }}
                </li>
              </ul>
            </div>
            <div v-else class="text-[var(--forebrain-muted-text)]">{{ t('chat.noTodos') }}</div>
            <ContextDebugPanel
              :data="sessionContextDebug"
              :loading="sessionContextLoading"
              :error="sessionContextError"
              @refresh="loadSessionContext"
            />
            <div>
              <div class="text-xs font-medium uppercase tracking-wide text-[var(--forebrain-muted-text)]">{{ t('chat.planMarkdown') }}</div>
              <pre v-if="sessionPlanText.trim()" class="mt-1 max-h-40 overflow-auto whitespace-pre-wrap rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-2 font-mono text-[12px] text-[var(--forebrain-text-2)]">{{ sessionPlanText }}</pre>
              <p v-else class="text-[var(--forebrain-muted-text)]">{{ t('chat.noPlan') }}</p>
            </div>
            <div>
              <div class="flex flex-wrap items-center justify-between gap-2">
                <div class="text-xs font-medium uppercase tracking-wide text-[var(--forebrain-muted-text)]">{{ t('chat.toolAuditRecent') }}</div>
                <!-- The one change the session can take back is its most recent
                     file edit, so that is what the action says it does. -->
                <button
                  v-if="toolAuditRows.some((row) => extractDiffPreview(row.detailJson))"
                  class="forebrain-btn forebrain-btn-ghost text-xs"
                  type="button"
                  :disabled="undoingFileChange"
                  data-testid="undo-last-file-change"
                  @click="undoLastFileChange"
                >{{ t('chat.undoLastFileChange') }}</button>
              </div>
              <p v-if="undoFileChangeResult" class="mt-1 text-[12px]" :class="undoFileChangeFailed ? 'text-[var(--forebrain-danger)]' : 'text-[var(--forebrain-text-2)]'" role="status">{{ undoFileChangeResult }}</p>
              <ul v-if="toolAuditRows.length" class="mt-1 space-y-2">
                <li v-for="row in toolAuditRows" :key="row.id" class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-2">
                  <div class="font-medium text-[var(--forebrain-text)]">{{ row.toolName }}</div>
                  <div v-if="toolAuditSummary(row.detailJson)" class="mt-1 break-all text-[12px] text-[var(--forebrain-text-2)]">{{ toolAuditSummary(row.detailJson) }}</div>
                  <div v-if="toolAuditError(row.detailJson)" class="mt-1 whitespace-pre-wrap break-words text-[12px] text-[var(--forebrain-danger)]">{{ toolAuditError(row.detailJson) }}</div>
                  <div v-if="extractDiffPreview(row.detailJson)" class="mt-2 rounded border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] p-2">
                    <div class="mb-1 text-[11px] font-medium text-[var(--forebrain-muted-text)]">{{ t('chat.diffPreview') }}</div>
                    <pre class="max-h-40 overflow-auto whitespace-pre-wrap text-[11px]">{{ extractDiffPreview(row.detailJson) }}</pre>
                  </div>
                  <div class="mt-2 flex flex-wrap gap-2">
                    <button
                      v-for="p in extractReferencedPaths(row.detailJson)"
                      :key="`${row.id}-${p}`"
                      class="forebrain-btn forebrain-btn-ghost text-xs"
                      type="button"
                      @click="insertWorkspaceRef(p)"
                    >
                      @{{ p }}
                    </button>
                  </div>
                </li>
              </ul>
              <p v-else class="text-[var(--forebrain-muted-text)]">{{ t('chat.noRecords') }}</p>
            </div>
            <div>
              <div class="text-xs font-medium uppercase tracking-wide text-[var(--forebrain-muted-text)]">{{ t('chat.costSummary') }}</div>
              <div v-if="sessionCost && (sessionCost.requests > 0 || sessionCost.toolCalls > 0)" class="mt-1 space-y-1 text-[12px] text-[var(--forebrain-text-2)]" data-testid="session-cost">
                <div v-if="sessionCost.requests > 0">{{ t(sessionCost.requests === 1 ? 'chat.costRequestsOne' : 'chat.costRequests', { input: compactTokenCount(sessionCost.inputTokens), output: compactTokenCount(sessionCost.outputTokens), requests: sessionCost.requests }) }}</div>
                <div v-if="sessionCost.cacheRead > 0 || sessionCost.cacheWritten > 0">{{ t('chat.costCache', { percent: sessionCost.cacheHitPercent, read: compactTokenCount(sessionCost.cacheRead), written: compactTokenCount(sessionCost.cacheWritten), uncached: compactTokenCount(sessionCost.uncached) }) }}</div>
                <div v-if="sessionCost.toolCalls > 0">
                  {{ t(sessionCost.toolCalls === 1 ? 'chat.costToolCallsOne' : 'chat.costToolCalls', { count: sessionCost.toolCalls }) }}
                  <span class="text-[var(--forebrain-muted-text)]">· {{ costByToolText(sessionCost.byTool) }}</span>
                </div>
              </div>
              <p v-else class="text-[var(--forebrain-muted-text)]">{{ t('chat.noCostSummary') }}</p>
            </div>
            <div>
              <div class="text-xs font-medium uppercase tracking-wide text-[var(--forebrain-muted-text)]">{{ t('chat.subagentHistory') }}</div>
              <ul v-if="subagentHistoryRecords.length" class="mt-1 space-y-2">
                <li
                  v-for="record in subagentHistoryRecords"
                  :key="`${record.taskId || record.runId || record.updatedAt}`"
                  class="rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-2"
				  :class="(record.agentId || record.taskId || record.runId) ? 'cursor-pointer hover:border-[var(--forebrain-brand-border)]' : ''"
				  @click="openSubagentHistory(record)"
                >
                  <div class="flex items-center justify-between gap-3">
                    <div class="min-w-0">
                      <div class="truncate text-sm font-medium text-[var(--forebrain-text)]">
                        {{ record.task || record.taskId || record.runId || 'Subagent' }}
                      </div>
                      <div class="truncate text-[11px] text-[var(--forebrain-muted-text)]">
                        {{ formatSubagentMeta(record) }}
                      </div>
                    </div>
                    <span class="rounded-full border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-2 py-0.5 text-[10px] uppercase tracking-wide text-[var(--forebrain-text-2)]">
                      {{ record.status || 'unknown' }}
                    </span>
                  </div>
                  <div
                    v-if="subagentPreview(record)"
                    class="mt-2 whitespace-pre-wrap rounded border border-[var(--forebrain-divider)] bg-[var(--forebrain-bg-alt)] p-2 font-mono text-[11px] text-[var(--forebrain-text-2)]"
                  >
                    {{ subagentPreview(record) }}
                  </div>
                </li>
              </ul>
              <p v-else class="text-[var(--forebrain-muted-text)]">{{ t('chat.noSubagentHistory') }}</p>
            </div>
          </div>
        </details>

        <div class="z-10 shrink-0 border-t border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-4 sm:px-5">
          <!--
            The MCP startup, while it is happening. It is live state pushed on its
            own socket op, and it is the answer to "why is the first turn waiting":
            the terminal shows the same line, so both surfaces say the same thing
            about the same runner.
          -->
          <div
            v-if="mcpStartupLabel"
            class="mb-3 flex items-center gap-3 text-[11px] uppercase tracking-[0.28em] text-[var(--forebrain-muted-text)]"
            aria-live="polite"
            data-field="mcp-startup"
          >
            <span class="h-px flex-1 bg-[var(--forebrain-rule-line)]" />
            <span class="min-w-0 text-center font-mono leading-relaxed">{{ mcpStartupLabel }}</span>
            <span class="h-px flex-1 bg-[var(--forebrain-rule-line)]" />
          </div>
          <Alert v-if="mcpFailures.length" variant="destructive" class="mb-3" data-field="mcp-failures">
            <CircleAlert class="size-4" />
            <AlertTitle>{{ t('chat.mcpFailedTitle') }}</AlertTitle>
            <AlertDescription class="space-y-1">
              <div v-for="row in mcpFailures" :key="row.name" class="whitespace-pre-wrap font-mono text-[11px]">
                {{ row.name }}: {{ row.error }}
              </div>
            </AlertDescription>
          </Alert>
          <div
            v-if="runtimeStatusLabel.label"
            class="mb-3 flex items-center gap-3 text-[11px] uppercase tracking-[0.28em] text-[var(--forebrain-muted-text)]"
            aria-live="polite"
          >
            <span class="h-px flex-1 bg-[var(--forebrain-rule-line)]" />
            <span class="flex min-w-0 items-center gap-3 font-mono leading-relaxed tracking-normal normal-case">
              {{ runtimeStatusLabel.label }}
              <PlanProgressSegments :plan="runtimeStatusLabel.plan" />
            </span>
            <span class="h-px flex-1 bg-[var(--forebrain-rule-line)]" />
          </div>
          <PendingActionsPanel
            :session-id="sessionId"
            :version="pendingActionsVersion"
            :context-used-percent="contextUsedPercent"
            can-open-agent
            @open-agent="openAgentView"
          />
          <PromptInputProvider :max-files="composerMaxFiles" accept="image/*,application/pdf" @submit="handleSubmit" @error="handleComposerError">
            <Alert v-if="composerNotice" variant="destructive" class="mb-2">
              <CircleAlert class="size-4" />
              <AlertTitle>{{ composerNotice.title }}</AlertTitle>
              <AlertDescription class="whitespace-pre-line">{{ composerNoticeDetail }}</AlertDescription>
            </Alert>
            <AutoContinueBanner :state="activeAutoContinue" @cancel="cancelAutoContinue(activeAgentView)" />
            <!-- The recommendation card owns its own goodbye: the answer's
                 result line stays up briefly, then the card closes itself. -->
            <LspRecommendationCard
              v-if="lspRecommendation"
              :recommendation="lspRecommendation"
              :session-id="sessionId ?? undefined"
              @close="lspRecommendation = null"
            />
            <div class="relative">
              <PendingInputQueuePopover
                :preview="composerPendingInput"
                @edit-last-queued="restoreLastQueuedMessage"
                @interrupt-run="interruptAndSendPendingSteers"
              />
              <PromptInput
                multiple
                global-drop
                class="w-full [&_[data-slot=input-group]]:border-[var(--forebrain-divider)] [&_[data-slot=input-group]]:bg-[var(--forebrain-surface)] [&_[data-slot=input-group]]:ring-0 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:border-[var(--forebrain-focus-border)] [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:border-[var(--forebrain-focus-border)] [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:ring-0 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:ring-0"
              >
                <PromptInputBody>
                  <ForebrainPromptTextarea ref="promptRef" :placeholder="inputPlaceholder" :bots="availableBots"
                    :bots-fetch-done="botsFetchDone" :during-run="isStreaming" :queue-visible="composerQueueVisible"
                    @queue-follow-up="markNextSubmissionAsQueued"
                    @edit-last-queued="restoreLastQueuedMessage"
                    @interrupt-run="interruptAndSendPendingSteers"
                    @typed="cancelAutoContinue(activeAgentView)"
                    @escape="cancelAutoContinue(activeAgentView)" />
                </PromptInputBody>
                <PromptInputFooter>
                  <div class="relative flex min-w-0 flex-1 items-center gap-1" ref="modeMenuRef">
                    <ApprovalPresetPicker :session-id="sessionId" />
                    <button
                      type="button"
                      class="inline-flex h-8 items-center gap-2 rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-2.5 text-xs font-medium text-[var(--forebrain-text-2)] transition hover:bg-[var(--forebrain-button-alt-bg)] disabled:cursor-not-allowed disabled:opacity-60"
                      :disabled="isStreaming"
                      @click="toggleModeMenu"
                    >
                      <span class="inline-flex h-3.5 w-3.5 items-center justify-center rounded-full border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] text-[9px] leading-none text-[var(--forebrain-brand-1)]">∞</span>
                      <span>{{ modeLabel }}</span>
                      <span class="text-[var(--forebrain-muted-text)]">▾</span>
                    </button>
                    <div
                      v-if="modeMenuOpen"
                      class="absolute bottom-full left-0 z-20 mb-2 min-w-[168px] rounded-xl border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] p-0.5 "
                    >
                      <button
                        v-for="item in modeOptions"
                        :key="item.id"
                        type="button"
                        class="flex h-8 w-full items-center justify-between rounded-lg px-2.5 py-1 text-left text-[13px] transition"
                        :class="[
                          item.enabled
                            ? 'text-[var(--forebrain-text)] hover:bg-[var(--forebrain-button-alt-bg)]'
                            : 'cursor-not-allowed text-[var(--forebrain-muted-text)]',
                        ]"
                        :disabled="!item.enabled || isStreaming"
                        @click="selectMode(item.id)"
                      >
                        <span>{{ item.label }}</span>
                        <span v-if="item.id === mode" class="text-[var(--forebrain-brand-1)]">✓</span>
                      </button>
                    </div>
                  </div>
                  <div class="flex items-center gap-1">
                    <PromptInputActionMenu>
                      <PromptInputActionMenuTrigger />
                      <PromptInputActionMenuContent>
                        <PromptInputActionAddAttachments :label="t('chat.chooseFile')" />
                      </PromptInputActionMenuContent>
                    </PromptInputActionMenu>
                    <PromptInputSubmit :status="submitStatus" />
                  </div>
                </PromptInputFooter>
              </PromptInput>
            </div>
          </PromptInputProvider>
        </div>
      </main>
      <ChatWorkbench
        v-if="workbench.open.value"
        :records="runningRoster"
        :loading="rosterLoading"
        :error="rosterError"
        :overlay="workbenchOverlay"
        @close="workbench.close()"
        @view="viewRoster"
        @cancel="cancelRoster"
        @cancel-all="cancelAll"
        @insert-ref="insertWorkspaceRef"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { MessageSquare, CheckCircle, CircleAlert, Bot, User, Image as ImageIcon, Paperclip, PanelRight } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { Avatar, AvatarFallback } from '@repo/shadcn-vue/components/ui/avatar'
import AgentViewTabs from '@/components/chat/AgentViewTabs.vue'
import SubagentCallCard from '@/components/chat/SubagentCallCard.vue'
import SubagentConversation from '@/components/chat/SubagentConversation.vue'
import SlashPickerCard from '@/components/chat/SlashPickerCard.vue'
import PlanUpdateCard from '@/components/chat/PlanUpdateCard.vue'
import ToolCallCard from '@/components/chat/ToolCallCard.vue'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import {
  Conversation,
  ConversationContent,
  ConversationEmptyState,
  ConversationScrollButton,
} from '@repo/elements/conversation'
import {
  Message,
  MessageContent,
  MessageResponse,
} from '@repo/elements/message'
import {
  Plan,
  PlanContent,
  PlanHeader,
  PlanTitle,
  PlanTrigger,
} from '@repo/elements/plan'
import {
  Task,
  TaskContent,
  TaskItem,
  TaskTrigger,
} from '@repo/elements/task'
import {
  PromptInput,
  PromptInputActionAddAttachments,
  PromptInputActionMenu,
  PromptInputActionMenuContent,
  PromptInputActionMenuTrigger,
  PromptInputBody,
  PromptInputFooter,
  PromptInputProvider,
  PromptInputSubmit,
} from '@repo/elements/prompt-input'
import ForebrainPromptTextarea, { type BotOption } from '@/components/ForebrainPromptTextarea.vue'
import ChatWorkbench from '@/components/chat/ChatWorkbench.vue'
import ApprovalPresetPicker from '@/components/chat/ApprovalPresetPicker.vue'
import PendingActionsPanel from '@/components/chat/PendingActionsPanel.vue'
import PlanProgressSegments from '@/components/chat/PlanProgressSegments.vue'
import RunWorkedLine from '@/components/chat/RunWorkedLine.vue'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import PendingInputQueuePopover from '@/components/PendingInputQueuePopover.vue'
import AutoContinueBanner from '@/components/chat/AutoContinueBanner.vue'
import LspRecommendationCard from '@/components/chat/LspRecommendationCard.vue'
import DiffView from '@/components/DiffView.vue'
import ContextDebugPanel from '@/components/ContextDebugPanel.vue'
import CompactionCard from '@/components/chat/CompactionCard.vue'
import GoalLine from '@/components/chat/GoalLine.vue'
import { FOREBRAIN_GOAL_CHECK_AGENT_TYPE } from '@/lib/forebrainGatewayRuntime'
import { agentRosterViewTarget, useAgentRoster } from '@/composables/useAgentRoster'
import { useChatStream, workedLineOf, emptyPendingInputPreview, type ChatMessage, type PlanBlock } from '@/composables/useChatStream'
import { useLastSession } from '@/composables/useLastSession'
import { useWorkbench } from '@/composables/useWorkbench'
import { useWorkspaceTree } from '@/composables/useWorkspaceTree'
const { refresh: workspaceTreeRefresh } = useWorkspaceTree()
import { useChatSessions } from '@/composables/useChatSessions'
import { useSessionInfo, sessionHeaderTitle } from '@/composables/useSessionInfo'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { parseJsonCamelCase } from '@/lib/case'
import { getErrorMessage, forebrainApi, type AgentRosterRow, type ChatAttachmentRecord, type SessionContextDebug, type SessionCostSummary, type SlashCommandRecord, type SubagentHistoryRecord, type ToolAuditRow } from '@/lib/api'
import { formatProviderError } from '@/lib/providerError'
import { uploadSubmission, type ComposerSubmission, type PickedFile } from '@/lib/composerSubmission'
import { buildContextDebugModel, compactTokenCount } from '@/lib/contextDebug'
import { useI18n } from '@/locales'
const { lastSessionId } = useLastSession()
const workbench = useWorkbench()

/** Only agents actually running reach the workbench card and the toggle's
 *  badge: an idle primary in the roster is not a live agent. */
const runningRoster = computed(() =>
  rosterRecords.value.filter((row) => String(row.status ?? '').trim().toLowerCase() === 'running'))

// Below lg the workbench overlays the conversation instead of narrowing it.
const workbenchOverlay = ref(false)
let workbenchMq: MediaQueryList | null = null
function syncWorkbenchOverlay() {
  workbenchOverlay.value = workbenchMq?.matches ?? false
}

/** The open conversation's own facts — title and project — read from the
 * session itself by id, not from the drawer's list. */
const { sessionInfo, loadSessionInfo } = useSessionInfo()

/** The project the open conversation belongs to, when it belongs to one. */
const sessionProject = computed(() => sessionInfo.value?.project ?? null)

/** The conversation's name, read from the session itself. */
const sessionTitle = computed(() => sessionHeaderTitle(sessionInfo.value, t('nav.chat')))
const { t, locale } = useI18n()
const router = useRouter()

const promptRef = ref<InstanceType<typeof ForebrainPromptTextarea> | null>(null)
// How many files the composer takes for one message.
const composerMaxFiles = 5
const availableBots = ref<BotOption[]>([])
const botsFetchDone = ref(true)

// A message the runtime sent on the person's behalf says who sent it, above
// the words; an origin this build does not know says nothing at all.
function originLabel(origin?: string): string {
  if (origin === 'heartbeat') return t('chat.originHeartbeat')
  if (origin === 'cron') return t('chat.originCron')
  return ''
}

function insertWorkspaceRef(path: string) {
  // Routed through the shared engine so a clicked image is attached rather
  // than named, exactly as picking it from the @ menu would be.
  void promptRef.value?.insertWorkspacePath?.(path)
}

function markNextSubmissionAsQueued() {
  nextSubmissionDisposition.value = 'queue'
}

async function restoreLastQueuedMessage() {
  const agentId = activeAgentView.value
  const submission = agentId ? await recallSubagentInput(agentId) : await editLastQueuedMessage()
  if (!submission) return
  promptRef.value?.restoreSubmission(submission)
}

async function interruptAndSendPendingSteers() {
  if (activeAgentView.value) {
    // A subagent's own queue: interrupt it precisely to send what waits.
    await interruptSubagentToSend(activeAgentView.value)
    return
  }
  sendPendingSteersAfterInterrupt()
}

const inputPlaceholder = computed(() => {
  // A subagent's view has its own composer, so its placeholder says who the
  // message is for.
  return activeAgentView.value ? t('chat.subagentComposerPlaceholder') : t('chat.placeholder')
})

const { sessions, fetchSessions } = useChatSessions()

const route = useRoute()
const { refreshToken, adoptActivePrimaryAgent, activeId: activePrimaryId } = usePrimaryAgents()

/**
 * Answers the picker a slash command put on a notice. A primary agent picked
 * from /agent is a tenant switch, which the rest of the page follows the way
 * it follows the agent switcher.
 */
async function pickFromNotice(msg: ChatMessage, value: string) {
  if (!msg.id || !msg.picker || msg.picked !== undefined) return
  await choose(msg.id, { command: msg.picker.command, value })
  if (msg.picker.command === 'agent') {
    // This conversation belongs to the agent being left; it leaves the
    // address bar before the page is rebuilt for the new tenant, the same
    // order the rail's switcher follows.
    if (value !== activePrimaryId.value && route.query.session) {
      await router.replace({ query: { ...route.query, session: undefined } })
    }
    await adoptActivePrimaryAgent()
  }
}
const {
  records: rosterRecords,
  loading: rosterLoading,
  error: rosterError,
  loadRoster,
  cancelRoster,
  cancelAll,
} = useAgentRoster()

const {
  sessionId,
  mode,
  isStreaming,
  error,
  messages,
  historyLoading,
  historyError,
  subagents,
  subagentCallTasks,
  subagentNow,
  runtimeStatus,
  mcpStatus,
  autoContinueForView,
  cancelAutoContinue,
  lspRecommendation,
  contextSignals,
  pendingActionsVersion,
  pendingInputPreview,
  subagentPendingInput,
  subagentReturnedDraft,
  takeSubagentReturnedDraft,
  sendToSubagent,
  recallSubagentInput,
  interruptSubagentToSend,
  withdrawSubagentInput,
  compactSubagent,
  loadSubagentContext,
  loadSubagentBudget,
  returnedDraft,
  takeReturnedDraft,
  takeReturnedNotice,
  takeReturnedNoticeCode,
  send,
  choose,
  editLastQueuedMessage,
  sendPendingSteersAfterInterrupt,
  loadMessages,
  switchToSession,
  formatRuntimeStatusLabel,
} = useChatStream()

const nextSubmissionDisposition = ref<'steer' | 'queue'>('steer')

// Which agent's conversation the thread is showing: '' is the primary agent.
const activeAgentView = ref('')
// The updatedSeq each subagent was at when the user last looked at it, so the
// tab strip can mark the ones that have said something since.
const seenAgentActivity = ref<Record<string, number>>({})

type ViewBrowseState = {
  scrollTop: number
  follow: boolean
  folds: Record<string, boolean>
}

type PersistedSessionBrowseState = {
  activeAgentView: string
  seenAgentActivity: Record<string, number>
  views: Record<string, ViewBrowseState>
}

const primaryViewKey = '$primary'
const conversationRef = ref<unknown>(null)
const viewBrowseStates = ref<Record<string, ViewBrowseState>>({})
let restoringBrowseState = false
let browsePersistTimer: ReturnType<typeof setTimeout> | null = null

function browseStorageKey(sid: string): string {
  return `forebrain:chat-browse:v1:${sid}`
}

function currentBrowseViewKey(): string {
  return activeAgentView.value || primaryViewKey
}

function conversationElement(): HTMLElement | null {
  const raw = conversationRef.value as { $el?: unknown } | HTMLElement | null
  if (!raw) return null
  const candidate = raw instanceof HTMLElement ? raw : raw.$el
  return candidate instanceof HTMLElement ? candidate : null
}

function defaultBrowseState(): ViewBrowseState {
  return { scrollTop: 0, follow: true, folds: {} }
}

function browseStateFor(agentId: string): ViewBrowseState {
  return viewBrowseStates.value[agentId || primaryViewKey] ?? defaultBrowseState()
}

function readSessionBrowseState(sid: string): PersistedSessionBrowseState {
  if (typeof window === 'undefined' || !sid) {
    return { activeAgentView: '', seenAgentActivity: {}, views: {} }
  }
  try {
    const parsed = JSON.parse(window.localStorage.getItem(browseStorageKey(sid)) ?? '{}') as Partial<PersistedSessionBrowseState>
    return {
      activeAgentView: String(parsed.activeAgentView ?? '').trim(),
      seenAgentActivity: parsed.seenAgentActivity && typeof parsed.seenAgentActivity === 'object'
        ? parsed.seenAgentActivity
        : {},
      views: parsed.views && typeof parsed.views === 'object' ? parsed.views : {},
    }
  } catch {
    return { activeAgentView: '', seenAgentActivity: {}, views: {} }
  }
}

function persistSessionBrowseState(sid = String(sessionId.value ?? '').trim()) {
  if (typeof window === 'undefined' || !sid) return
  try {
    window.localStorage.setItem(browseStorageKey(sid), JSON.stringify({
      activeAgentView: activeAgentView.value,
      seenAgentActivity: seenAgentActivity.value,
      views: viewBrowseStates.value,
    } satisfies PersistedSessionBrowseState))
  } catch {
    // Browsing remains functional when storage is unavailable or full.
  }
}

function scheduleBrowsePersist() {
  if (browsePersistTimer) clearTimeout(browsePersistTimer)
  browsePersistTimer = setTimeout(() => {
    browsePersistTimer = null
    persistSessionBrowseState()
  }, 80)
}

function recordCurrentBrowseState() {
  if (restoringBrowseState) return
  const el = conversationElement()
  if (!el) return
  const key = currentBrowseViewKey()
  const prior = viewBrowseStates.value[key] ?? defaultBrowseState()
  viewBrowseStates.value = {
    ...viewBrowseStates.value,
    [key]: {
      ...prior,
      scrollTop: Math.max(0, el.scrollTop),
      follow: el.scrollHeight - el.clientHeight - el.scrollTop <= 32,
    },
  }
  scheduleBrowsePersist()
}

async function restoreCurrentBrowseState() {
  await nextTick()
  const el = conversationElement()
  if (!el) return
  const state = browseStateFor(activeAgentView.value)
  restoringBrowseState = true
  el.scrollTop = state.follow ? el.scrollHeight : Math.min(state.scrollTop, Math.max(0, el.scrollHeight - el.clientHeight))
  window.setTimeout(() => { restoringBrowseState = false }, 0)
}

function updateAgentFold(agentId: string, value: { id: string; open: boolean }) {
  const key = String(agentId ?? '').trim()
  const foldID = String(value?.id ?? '').trim()
  if (!key || !foldID) return
  const prior = viewBrowseStates.value[key] ?? defaultBrowseState()
  viewBrowseStates.value = {
    ...viewBrowseStates.value,
    [key]: { ...prior, folds: { ...prior.folds, [foldID]: Boolean(value.open) } },
  }
  scheduleBrowsePersist()
}

const activeSubagent = computed(() =>
  subagents.value.find((entry) => entry.agentId === activeAgentView.value) ?? null,
)

// The continuation the composer's own view waits on: a subagent's while its
// view is open, the conversation's otherwise. One composer, two owners.
const activeAutoContinue = computed(() => autoContinueForView(activeAgentView.value))

// The queue preview the composer shows: a subagent's own while its view is
// open, the conversation's otherwise. One composer, two owners.
const composerPendingInput = computed(() => (
  activeAgentView.value
    ? (subagentPendingInput.value[activeAgentView.value] ?? emptyPendingInputPreview())
    : pendingInputPreview.value
))

// Whether the composer's queue preview shows anything: recalling the newest
// message is offered exactly as long as one is shown.
const composerQueueVisible = computed(() => {
  const preview = composerPendingInput.value
  return preview.pendingSteers.length + preview.rejectedSteers.length + preview.queuedMessages.length > 0
})

// Composer drafts belong to the view they were typed in: switching between the
// conversation and a subagent's own view saves one and restores the other
// (plan 007's rule, kept here the same way).
const viewDrafts = ref<Record<string, ComposerSubmission>>({})

// The commands a subagent's own view may run, by name and class, from the
// server's own classification (plan 007's D4). Skill commands act on the
// subagent; the rest belong to or are hidden from its view.
const subagentSlashRecords = ref<SlashCommandRecord[]>([])

// /context for a subagent's view, drawn with the conversation's own panel.
const subagentContextTarget = ref('')
const subagentContextDebug = ref<SessionContextDebug | null>(null)
const subagentContextLoading = ref(false)

/** The command name a slash line begins with, lowercase; '' when it is not one. */
function slashCommandName(text: string): string {
  const trimmed = text.trim()
  if (!trimmed.startsWith('/')) return ''
  return (trimmed.slice(1).split(/\s+/, 1)[0] ?? '').toLowerCase()
}

function markAgentSeen(agentId: string) {
  const entry = subagents.value.find((row) => row.agentId === agentId)
  if (!entry) return
  seenAgentActivity.value = { ...seenAgentActivity.value, [agentId]: entry.updatedSeq }
}

/**
 * The composer's own draft, whole: its text and everything it attached. The
 * composer keeps its state inside itself, so the draft is read back through
 * the textarea it renders — the alternative, an accessor on that component, is
 * outside this plan's file scope.
 */
function takeComposerDraft(): ComposerSubmission {
  const root = (promptRef.value as unknown as { $el?: HTMLElement } | null)?.$el
  const text = root?.querySelector('textarea')?.value ?? ''
  const attached = promptRef.value?.takeAttached() ?? { attachments: [], mentionImages: [] }
  return { text, attachments: attached.attachments, mentionImages: attached.mentionImages }
}

/** Puts a view's draft back into the composer, text and attachments alike. */
function applyComposerDraft(draft: ComposerSubmission) {
  const root = (promptRef.value as unknown as { $el?: HTMLElement } | null)?.$el
  const el = root?.querySelector('textarea')
  if (el) {
    el.value = draft.text
    el.dispatchEvent(new Event('input', { bubbles: true }))
  }
  if (draft.attachments.length || draft.mentionImages.length) {
    promptRef.value?.restoreSubmission({ text: '', attachments: draft.attachments, mentionImages: draft.mentionImages })
  }
}

/** A message the boundary gave back to this view's composer, if any is waiting. */
function applySubagentReturnedDraft(agentId: string) {
  const draft = takeSubagentReturnedDraft(agentId)
  if (draft) promptRef.value?.restoreSubmission(draft)
}

/** The commands a subagent's own view may run, from the server's classification. */
async function refreshSubagentSlashCommands() {
  try {
    const res = await forebrainApi.slashCommands('webchat', '', { subagentView: true })
    subagentSlashRecords.value = Array.isArray(res.records) ? res.records : []
  } catch {
    subagentSlashRecords.value = []
  }
}

function openAgentView(agentId: string) {
  const from = activeAgentView.value
  if (from !== agentId) {
    // The draft belongs to the view it was typed in; the composer carries the
    // new view's own draft, text and attachments alike.
    viewDrafts.value = { ...viewDrafts.value, [from]: takeComposerDraft() }
    applyComposerDraft(viewDrafts.value[agentId] ?? { text: '', attachments: [], mentionImages: [] })
  }
  recordCurrentBrowseState()
  activeAgentView.value = agentId
  if (agentId) markAgentSeen(agentId)
  scheduleBrowsePersist()
  void restoreCurrentBrowseState()
  if (agentId) {
    void loadSubagentBudget(agentId)
    void refreshSubagentSlashCommands()
    applySubagentReturnedDraft(agentId)
  } else {
    subagentContextTarget.value = ''
    subagentContextDebug.value = null
  }
}

function openSubagentHistory(record: SubagentHistoryRecord) {
	const id = String(record.agentId || record.taskId || record.runId || '').trim()
	if (id) openAgentView(id)
}

// Staying on a subagent's view keeps it marked read as it works, so the dot
// means "said something while you were elsewhere" and nothing else.
watch(subagents, () => {
  if (activeAgentView.value) markAgentSeen(activeAgentView.value)
}, { deep: true })

// A subagent from a previous session is not this session's; fall back to the
// conversation rather than showing an empty screen.
watch(sessionId, (sid, previousSid) => {
  // A synchronous watch runs before useChatStream clears the old message DOM,
  // so the last scroll anchor is still measurable during A -> B switches.
  if (previousSid) {
    recordCurrentBrowseState()
    persistSessionBrowseState(previousSid)
  }
  const restored = readSessionBrowseState(String(sid ?? '').trim())
  activeAgentView.value = restored.activeAgentView
  seenAgentActivity.value = restored.seenAgentActivity
  viewBrowseStates.value = restored.views
  void restoreCurrentBrowseState()
}, { flush: 'sync' })

watch(activeSubagent, () => {
  // Covers a saved agent view becoming available after its event pages load.
  void restoreCurrentBrowseState()
})

/**
 * Esc in a subagent's own view (D1): take a just-sent message back before it
 * answered; otherwise interrupt it to send what is queued behind it;
 * otherwise leave for the conversation. The same three meanings, in the same
 * order, as the terminal.
 */
async function handleAgentViewEscape() {
  const agentId = activeAgentView.value
  if (!agentId) return
  const withdrawn = await withdrawSubagentInput(agentId)
  if (withdrawn.length) {
    for (const submission of withdrawn) promptRef.value?.restoreSubmission(submission)
    return
  }
  if (await interruptSubagentToSend(agentId)) return
  openAgentView('')
}

function handleAgentViewKeydown(evt: KeyboardEvent) {
  if (evt.key === 'Escape' && activeAgentView.value) {
    void handleAgentViewEscape()
  }
}

onMounted(() => window.addEventListener('keydown', handleAgentViewKeydown))
onUnmounted(() => {
  recordCurrentBrowseState()
  persistSessionBrowseState()
  if (browsePersistTimer) clearTimeout(browsePersistTimer)
  window.removeEventListener('keydown', handleAgentViewKeydown)
})

const modeLabel = computed(() => {
  if (mode.value === 'plan') return t('chat.modePlan')
  return t('chat.modeAgent')
})
const runtimeStatusLabel = computed(() => formatRuntimeStatusLabel(runtimeStatus.value, t))
// A goal's checks are reached from the goal's own lines, one per round; they
// take no tab of their own, except the one being read.
const agentTabRecords = computed(() => subagents.value.filter((record) => (
  record.agentType !== FOREBRAIN_GOAL_CHECK_AGENT_TYPE || record.agentId === activeAgentView.value
)))

/**
 * The MCP startup line: how many servers have settled, and which ones the
 * session is still waiting on. Empty once the generation settles — the line
 * describes a wait, and there is nothing to wait for afterwards.
 */
const mcpStartupLabel = computed(() => {
  const status = mcpStatus.value
  if (!status || !status.pending) return ''
  const connecting = status.servers.filter((row) => row.connStatus === 'connecting')
  if (!connecting.length) return ''
  return t('chat.mcpStarting', {
    settled: status.servers.length - connecting.length,
    total: status.servers.length,
    names: connecting.map((row) => row.name).join(', '),
  })
})

/**
 * The servers that failed to start, with the text the server itself produced.
 * A gateway session has no status line to read afterwards, so this is where a
 * failure stays visible; the text is never rewritten, because it is the only
 * copy that came from the server.
 */
const mcpFailures = computed(() =>
  (mcpStatus.value?.servers ?? [])
    .filter((row) => row.connStatus === 'error' && (row.error ?? '').trim())
    .map((row) => ({ name: row.name, error: (row.error ?? '').trim() })),
)
const modeMenuOpen = ref(false)
const modeMenuRef = ref<HTMLElement | null>(null)
const modeOptions = [
  { id: 'agent', label: 'Agent', enabled: true },
  { id: 'plan', label: 'Plan', enabled: true },
] as const

const sessionTodosList = ref<{ id: string; content: string; status: string; updatedAt: number }[]>([])
const sessionPlanText = ref('')
const toolAuditRows = ref<ToolAuditRow[]>([])
const sessionCost = ref<SessionCostSummary | null>(null)
const undoingFileChange = ref(false)
const undoFileChangeResult = ref('')
const undoFileChangeFailed = ref(false)
const subagentHistoryRecords = ref<SubagentHistoryRecord[]>([])
const sessionContextDebug = ref<SessionContextDebug | null>(null)
const sessionContextLoading = ref(false)
const sessionContextError = ref<string | null>(null)
let sessionContextGeneration = 0
let sessionWorkspaceGeneration = 0
const sessionContextSummary = computed(() => {
  const model = buildContextDebugModel(sessionContextDebug.value)
  const signalLabel = model.signal === 'warn'
    ? t('chat.contextSignalWarn')
    : model.signal === 'block'
      ? t('chat.contextSignalBlock')
      : t('chat.contextSignalOk')
  const signalClass = model.tone === 'warning'
    ? 'border-[var(--forebrain-warning)] bg-[var(--forebrain-warning-soft)] text-[var(--forebrain-warning)]'
    : model.tone === 'danger'
      ? 'border-[var(--forebrain-danger)] bg-[var(--forebrain-bg-alt)] text-[var(--forebrain-danger)]'
      : 'border-[var(--forebrain-success)] bg-[var(--forebrain-success-soft)] text-[var(--forebrain-success)]'
  const line = sessionContextDebug.value
    ? `${t('chat.contextEstimate')} ${compactTokenCount(model.estimate)} · ${t('chat.contextRemaining')} ${compactTokenCount(model.remaining)} · ${t('chat.contextEvictions')} ${model.evictionCount}`
    : t('chat.noContextDebug')
  return { signalLabel, signalClass, line }
})

/**
 * How full the context window is, from the newest usage number the
 * conversation holds — the same figure the footer's gauge is drawn from, and
 * what the exit-plan card's clear-context choice says it would free. Null
 * before the first turn reports a budget.
 */
const contextUsedPercent = computed(() => {
  for (let i = messages.value.length - 1; i >= 0; i--) {
    const left = messages.value[i]?.tokenBudget?.percentLeft
    if (typeof left === 'number' && Number.isFinite(left)) {
      return Math.max(0, Math.min(100, 100 - left))
    }
  }
  return null
})

async function loadSessionContext() {
  const sid = sessionId.value
	const generation = ++sessionContextGeneration
  if (!sid) {
    sessionContextDebug.value = null
    sessionContextError.value = null
    sessionContextLoading.value = false
    return
  }
  sessionContextLoading.value = true
  sessionContextError.value = null
  try {
    const data = await forebrainApi.sessionContext(sid, {
      runId: contextSignals.value.activeRunId,
    })
	if (generation !== sessionContextGeneration || sessionId.value !== sid) return
	sessionContextDebug.value = data && Object.keys(data).length ? data : null
  } catch (err) {
	if (generation !== sessionContextGeneration || sessionId.value !== sid) return
    sessionContextDebug.value = null
    sessionContextError.value = err instanceof Error ? err.message : t('chat.contextLoadFailed')
  } finally {
	  if (generation === sessionContextGeneration && sessionId.value === sid) sessionContextLoading.value = false
  }
}

async function loadSessionWorkspace() {
  const sid = sessionId.value
	const generation = ++sessionWorkspaceGeneration
  if (!sid) {
    sessionTodosList.value = []
    sessionPlanText.value = ''
    toolAuditRows.value = []
    sessionCost.value = null
    subagentHistoryRecords.value = []
    sessionContextDebug.value = null
    sessionContextError.value = null
    return
  }
	await loadSessionContext()
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
  try {
    const todos = await forebrainApi.sessionTodos(sid)
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    sessionTodosList.value = todos.items ?? []
  } catch {
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    sessionTodosList.value = []
  }
  try {
    const pl = await forebrainApi.sessionPlanMd(sid)
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    sessionPlanText.value = pl.markdown ?? ''
  } catch {
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    sessionPlanText.value = ''
  }
  try {
    const rows = await forebrainApi.sessionToolAudit(sid, 40)
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    toolAuditRows.value = Array.isArray(rows) ? rows : []
  } catch {
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    toolAuditRows.value = []
  }
  try {
    const c = await forebrainApi.sessionCostSummary(sid)
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    sessionCost.value = c
  } catch {
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    sessionCost.value = null
  }
  try {
    const sh = await forebrainApi.sessionSubagentHistory(sid)
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    subagentHistoryRecords.value = Array.isArray(sh.records) ? sh.records : []
  } catch {
	if (generation !== sessionWorkspaceGeneration || sessionId.value !== sid) return
    subagentHistoryRecords.value = []
  }
}

function formatSubagentMeta(record: SubagentHistoryRecord): string {
  const parts: string[] = []
  if (record.taskId) parts.push(`task=${record.taskId}`)
  if (record.runId) parts.push(`run=${record.runId}`)
  if (record.parentRunId) parts.push(`parent=${record.parentRunId}`)
  if (record.updatedAt) parts.push(`updated=${formatUnixSeconds(record.updatedAt)}`)
  return parts.join('  ')
}

function subagentPreview(record: SubagentHistoryRecord): string {
  const raw = (record.error || record.output || '').trim()
  if (!raw) return ''
  return raw.length > 280 ? `${raw.slice(0, 280)}...` : raw
}

function formatUnixSeconds(value?: number): string {
  if (!value || !Number.isFinite(value)) return ''
  return new Date(value * 1000).toLocaleString()
}

// extractDiffPreview reads the diff a completed edit_file/write_file event
// carries: the engine's own turn-diff envelope when there is one, else the
// call's old/new text as the tool recorded it in toolMeta.input.
function extractDiffPreview(detailJson: string): string {
  try {
    const obj = parseJsonCamelCase<Record<string, unknown>>(detailJson)
    const output = (obj.output ?? {}) as Record<string, unknown>
    const turnDiff = (output.turnDiff ?? {}) as Record<string, unknown>
    // Shown whole: the preview scrolls rather than cutting a change short.
    const unified = typeof turnDiff.unifiedDiff === 'string' ? turnDiff.unifiedDiff : ''
    if (unified) return unified
    const input = toolCallInput(obj)
    const oldStr = typeof input.oldString === 'string' ? input.oldString : ''
    const newStr = typeof input.newString === 'string' ? input.newString : ''
    if (oldStr || newStr) {
      return `--- old\n${oldStr}\n+++ new\n${newStr}`
    }
    return ''
  } catch {
    return ''
  }
}

// toolCallInput is the map a completed tool event recorded as the call's
// arguments, empty when the payload carries none.
function toolCallInput(obj: Record<string, unknown>): Record<string, unknown> {
  const meta = (obj.toolMeta ?? {}) as Record<string, unknown>
  const input = meta.input
  return typeof input === 'object' && input !== null ? (input as Record<string, unknown>) : {}
}

function extractReferencedPaths(detailJson: string): string[] {
  try {
    const input = toolCallInput(parseJsonCamelCase<Record<string, unknown>>(detailJson))
    const out: string[] = []
    const keys = ['filePath', 'path', 'absPath']
    for (const k of keys) {
      const v = input[k]
      if (typeof v === 'string' && v.trim()) {
        const p = v.replace(/^.*workspace\//, '').replace(/^\/+/, '')
        out.push(p)
      }
    }
    return Array.from(new Set(out)).slice(0, 6)
  } catch {
    return []
  }
}

// toolAuditSummary is what a recorded call did, in the words its card uses —
// never the payload it was recorded as.
function toolAuditSummary(detailJson: string): string {
  try {
    const obj = parseJsonCamelCase<Record<string, unknown>>(detailJson)
    const summary = typeof obj.summary === 'string' ? obj.summary.trim() : ''
    if (summary) return summary
    const description = typeof obj.description === 'string' ? obj.description.trim() : ''
    if (description) return description
    const meta = (obj.toolMeta ?? {}) as Record<string, unknown>
    return typeof meta.invocation === 'string' ? meta.invocation.trim() : ''
  } catch {
    return ''
  }
}

function toolAuditError(detailJson: string): string {
  try {
    const obj = parseJsonCamelCase<Record<string, unknown>>(detailJson)
    return typeof obj.error === 'string' ? obj.error.trim() : ''
  } catch {
    return ''
  }
}

function costByToolText(byTool: Record<string, number>): string {
  return Object.entries(byTool ?? {})
    .sort((a, b) => b[1] - a[1] || a[0].localeCompare(b[0]))
    .map(([name, count]) => `${name} × ${count}`)
    .join(' · ')
}

// undoLastFileChange restores the file the session changed most recently to
// what it held before that change, and says which file it restored — or, in
// the gateway's own words, why it could not.
async function undoLastFileChange() {
  const sid = sessionId.value
  if (!sid || undoingFileChange.value) return
  undoingFileChange.value = true
  undoFileChangeResult.value = ''
  undoFileChangeFailed.value = false
  try {
    const res = await forebrainApi.sessionRewindLast(sid)
    undoFileChangeResult.value = t('chat.undoneFileChange', { path: String(res.absPath ?? '') })
    await loadSessionWorkspace()
  } catch (cause) {
    undoFileChangeFailed.value = true
    undoFileChangeResult.value = getErrorMessage(cause)
  } finally {
    undoingFileChange.value = false
  }
}

watch(sessionId, () => {
  void loadSessionWorkspace()
  void loadRoster()
})

watch(
  () => [sessionId.value, contextSignals.value.compactVersion, contextSignals.value.budgetVersion],
  ([sid], [prevSid, prevCompact, prevBudget]) => {
    if (!sid) return
    if (sid !== prevSid || contextSignals.value.compactVersion !== prevCompact || contextSignals.value.budgetVersion !== prevBudget) {
      void loadSessionContext()
    }
  },
)

const submitStatus = computed(() => (isStreaming.value ? 'streaming' : 'ready'))

function viewRoster(row: AgentRosterRow) {
  const target = agentRosterViewTarget(row)
  if (!target) return
  void router.replace(target)
}

function submitPlanCommand(cmd: string) {
  if (isStreaming.value) return
  send(cmd, {
    sessionId: sessionId.value ?? undefined,
  })
}

function enterPlanMode() {
  submitPlanCommand('/plan')
}

function exitPlanMode() {
  submitPlanCommand('/plan mode off')
}

function toggleModeMenu() {
  modeMenuOpen.value = !modeMenuOpen.value
}

function selectMode(next: string) {
  if (next === 'agent') {
    modeMenuOpen.value = false
    if (mode.value !== 'agent') exitPlanMode()
    return
  }
  if (next === 'plan') {
    modeMenuOpen.value = false
    if (mode.value !== 'plan') enterPlanMode()
    return
  }
}

function handleWindowPointerDown(event: MouseEvent) {
  if (!modeMenuOpen.value) return
  const target = event.target as Node | null
  if (!target) return
  if (modeMenuRef.value && modeMenuRef.value.contains(target)) return
  modeMenuOpen.value = false
}

function getPlanBlocks(msg: { plan?: { question?: string; steps?: unknown[] } | null; planBlocks?: PlanBlock[] }): PlanBlock[] {
  if (msg.planBlocks && msg.planBlocks.length > 0) {
    return msg.planBlocks
  }
  if (msg.plan) {
    return [{ plan: msg.plan as PlanBlock['plan'] }]
  }
  return []
}

function messageBubbleRole(role: string): 'user' | 'assistant' {
  return role === 'user' ? 'user' : 'assistant'
}

/**
 * messageHasBubble reports whether a stored row has anything to say in a bubble.
 *
 * A row that only issued a tool call carries no text at all: its card is what
 * the row means, and an empty bubble beside that card is just noise. The same
 * goes for a row whose only content is a card the timeline already draws.
 */
function messageHasBubble(msg: ChatMessage): boolean {
  if (String(msg.content ?? '').trim()) return true
  if (msg.attachments?.length) return true
  if (msg.role !== 'assistant') return false
  return Boolean(
    msg.blocks?.length ||
    msg.planUpdates?.length ||
    msg.planBlocks?.length ||
    msg.turnDiffs?.length ||
    workedLineOf(msg, t).label,
  )
}

function messageClass(role: string): string | undefined {
  return role === 'user' ? undefined : 'max-w-full overflow-visible'
}

function messageContentClass(role: string): string | undefined {
  if (role === 'user') return undefined
  return 'w-full min-w-0 max-w-full overflow-visible break-words [&_pre]:max-w-full [&_pre]:overflow-x-auto [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto'
}

function reasoningDisplayText(content: string): string {
  const text = String(content ?? '').trim()
  if (!text) return ''
  const lines = text.split(/\r?\n/)
  return lines.map((line, idx) => `${idx === 0 ? '▸ ' : '  '}${line}`).join('\n')
}

/** An attachment the model was shown as an image: an image upload, or an image picked from the workspace. */
function isImageAttachment(attachment: ChatAttachmentRecord): boolean {
  return Boolean(attachment.path) || String(attachment.mediaType ?? '').startsWith('image/')
}

function attachmentUrl(fileId: string): string {
  return `/api/files/${encodeURIComponent(fileId)}/download`
}

function isAllowedAttachmentType(mediaType: string, filename: string): boolean {
  const t = (mediaType ?? '').toLowerCase()
  const name = (filename ?? '').toLowerCase()
  return t.startsWith('image/') || t === 'application/pdf' || /\.(png|jpe?g|gif|webp|pdf)$/i.test(name)
}

/**
 * Sends what the composer submitted in a subagent's own view to that subagent.
 * Everything typed there belongs to the subagent: ordinary text runs its next
 * execution, a skill command is sent to it, /compact and /context act on it,
 * a command that acts on the conversation runs there, and the ones hidden
 * answer with one sentence — plan 007's D4, in this surface.
 */
async function handleSubagentSubmit(message: { text: string; files?: { url?: string; filename?: string; mediaType?: string; file?: File }[] }) {
  const agentId = activeAgentView.value
  if (!agentId) return
  const text = (message.text ?? '').trim()
  const files = message.files ?? []
  const attachedByComposer = promptRef.value?.hasAttached() ?? false
  if (!text && files.length === 0 && !attachedByComposer) return
  // A shell line is the conversation's, never the subagent's.
  if (text.startsWith('!')) {
    composerNotice.value = { title: t('chat.messageNotSent'), detail: t('chat.subagentViewCommand') }
    return
  }
  if (text.startsWith('/')) {
    const name = slashCommandName(text)
    if (name === 'compact') {
      await compactActiveSubagent()
      return
    }
    if (name === 'context') {
      await openSubagentContext(agentId)
      return
    }
    const record = subagentSlashRecords.value.find((row) => String(row.name ?? '').toLowerCase() === name)
    if (record && record.category !== 'skill') {
      // Runs exactly as it does in the conversation's view: the reply lands
      // there, where the command acted.
      nextSubmissionDisposition.value = 'steer'
      await send(text, { sessionId: sessionId.value ?? undefined, activeInputDisposition: undefined })
      return
    }
    if (!record) {
      composerNotice.value = { title: t('chat.messageNotSent'), detail: t('chat.subagentViewCommand') }
      return
    }
    // A skill command: sent raw; the gateway expands it for this subagent.
  }
  const attached = promptRef.value?.takeAttached() ?? { attachments: [], mentionImages: [] }
  composerNotice.value = null
  const queued = nextSubmissionDisposition.value === 'queue'
  nextSubmissionDisposition.value = 'steer'
  const picked: PickedFile[] = files
    .filter((f) => isAllowedAttachmentType(f.mediaType ?? '', f.filename ?? ''))
    .flatMap((f) => (f.file ? [{ file: f.file, filename: f.filename || f.file.name, mediaType: f.mediaType || f.file.type }] : []))
  let uploadSession: Promise<string> | null = null
  const sessionForUpload = () => {
    uploadSession ??= sessionId.value
      ? Promise.resolve(sessionId.value)
      : forebrainApi.chatSessionCreate().then((created) => created.id)
    return uploadSession
  }
  const { submission, unsent, failure } = await uploadSubmission(text, attached, picked,
    async (file) => forebrainApi.filesUpload(file, await sessionForUpload()))
  if (failure) {
    composerNotice.value = {
      title: t('chat.messageNotSent'),
      detail: `${t('chat.uploadFailed', { name: failure.filename })}\n${getErrorMessage(failure.error)}`,
    }
    promptRef.value?.restoreSubmission(submission, unsent)
    return
  }
  await sendToSubagent(agentId, submission, queued)
}

/** /compact in a subagent's view: its own context, refused while it runs. */
async function compactActiveSubagent() {
  const agentId = activeAgentView.value
  if (!agentId) return
  const outcome = await compactSubagent(agentId)
  if (outcome === 'running') {
    composerNotice.value = { title: t('chat.messageNotSent'), detail: t('chat.subagentCompactRunning') }
  }
}

/** /context in a subagent's view, drawn with the conversation's own panel. */
async function openSubagentContext(agentId: string) {
  const id = String(agentId ?? '').trim()
  if (!id) return
  subagentContextTarget.value = id
  subagentContextLoading.value = true
  subagentContextDebug.value = null
  try {
    const data = await loadSubagentContext(id)
    if (subagentContextTarget.value === id) subagentContextDebug.value = data
  } finally {
    if (subagentContextTarget.value === id) subagentContextLoading.value = false
  }
}

async function handleSubmit(message: { text: string; files?: { url?: string; filename?: string; mediaType?: string; file?: File }[] }) {
  // The composer belongs to whichever view is on screen: a subagent's own view
  // sends to that subagent, never to the conversation (rule 9).
  if (activeAgentView.value) {
    await handleSubagentSubmit(message)
    return
  }
  const text = (message.text ?? '').trim()
  const files = message.files ?? []
  const attachedByComposer = promptRef.value?.hasAttached() ?? false
  if (!text && files.length === 0 && !attachedByComposer) return
  if (text === '/memories' && files.length === 0 && !attachedByComposer && !isStreaming.value) {
    await router.push({ name: 'memories' })
    return
  }
  // Everything the composer attached leaves it with this message at once, so
  // an image picked while the files upload belongs to the next one.
  const attached = promptRef.value?.takeAttached() ?? { attachments: [], mentionImages: [] }
  composerNotice.value = null
  const activeInputDisposition = isStreaming.value ? nextSubmissionDisposition.value : undefined
  nextSubmissionDisposition.value = 'steer'
  const picked: PickedFile[] = files
    .filter((f) => isAllowedAttachmentType(f.mediaType ?? '', f.filename ?? ''))
    .flatMap((f) => (f.file ? [{ file: f.file, filename: f.filename || f.file.name, mediaType: f.mediaType || f.file.type }] : []))
  // A file belongs to a conversation, so the first message of a new chat opens
  // its session before the first upload, once, and is then sent into it.
  let uploadSession: Promise<string> | null = null
  const sessionForUpload = () => {
    uploadSession ??= sessionId.value
      ? Promise.resolve(sessionId.value)
      : forebrainApi.chatSessionCreate().then((created) => created.id)
    return uploadSession
  }
  const { submission, unsent, failure } = await uploadSubmission(text, attached, picked,
    async (file) => forebrainApi.filesUpload(file, await sessionForUpload()))
  if (failure) {
    // The message waits in the composer, whole, until its files upload.
    composerNotice.value = {
      title: t('chat.messageNotSent'),
      detail: `${t('chat.uploadFailed', { name: failure.filename })}\n${getErrorMessage(failure.error)}`,
    }
    promptRef.value?.restoreSubmission(submission, unsent)
    return
  }
  await send(text, {
    // The address bar is the one source of which session is open. The
    // composer's submit can land before the route watcher has applied a
    // just-pushed session switch, so read the target here rather than
    // trusting the (possibly one-tick-stale) sessionId ref.
    sessionId: uploadSession ? await uploadSession : (String(route.query.session ?? '').trim() || sessionId.value || undefined),
    attached: submission,
    activeInputDisposition,
  })
}

// A message the boundary handed back to a subagent's composer while that view
// is open goes straight back into it; when the view is elsewhere it waits under
// its id until the view opens (openAgentView restores it).
watch(subagentReturnedDraft, () => {
  const agentId = activeAgentView.value
  if (!agentId) return
  applySubagentReturnedDraft(agentId)
}, { deep: true })

// A message that never became a turn — withdrawn before it began, handed back
// by a run that ended before taking it, or not taken by a run that had just
// ended — is the user's draft again, with everything it attached.
watch(returnedDraft, () => {
  const draft = takeReturnedDraft()
  if (draft) promptRef.value?.restoreSubmission(draft)
  const notice = takeReturnedNotice()
  const noticeCode = takeReturnedNoticeCode()
  if (notice) composerNotice.value = { title: t('chat.messageNotSent'), detail: notice, ...(noticeCode ? { detailCode: noticeCode } : {}) }
})

/** What the composer has to tell the user about a message or file it kept. */
const composerNotice = ref<{ title: string; detail: string; detailCode?: string } | null>(null)
// A refusal the runtime classified carries its code: its sentence is written
// when the notice is drawn, in the language the viewer is reading then —
// including a switch made while the notice is on screen — the way a run's
// error block does. The runtime's own sentence shows only when the code is
// not one this build knows.
const composerNoticeDetail = computed(() => {
  const notice = composerNotice.value
  if (!notice) return ''
  const code = notice.detailCode?.trim()
  return (code ? formatProviderError({ code }, locale.value) : null) ?? notice.detail
})

/**
 * The composer turned files away as they were added — past the file limit, or
 * of a type it does not accept. The user hears about it; nothing is dropped
 * without a word.
 */
function handleComposerError(err: { code: string; message: string }) {
  const detail = err.code === 'max_files'
    ? t('chat.attachTooMany', { max: composerMaxFiles })
    : err.code === 'accept'
      ? t('chat.attachUnsupported')
      : err.message
  composerNotice.value = { title: t('chat.fileNotAdded'), detail }
}

watch(isStreaming, (streaming, wasStreaming) => {
  if (wasStreaming && !streaming) {
    fetchSessions()
    // A finished turn may have changed the workspace; refresh the tree only
    // when it has been loaded at all (the workbench was opened once).
    void workspaceTreeRefresh()
    void loadSessionWorkspace()
    void loadRoster()
  }
})

watch(refreshToken, () => {
  void loadRoster()
})

onMounted(async () => {
  window.addEventListener('pointerdown', handleWindowPointerDown)
  workbenchMq = window.matchMedia('(max-width: 1023px)')
  syncWorkbenchOverlay()
  workbenchMq.addEventListener('change', syncWorkbenchOverlay)
  void fetchSessions()
  void loadRoster()
  await loadSessionWorkspace()
  const qSid = String(route.query.session ?? '').trim()
  if (!qSid) {
    const lid = lastSessionId.value
    if (lid && lid.trim()) {
      void router.replace({ query: { ...route.query, session: lid } })
      return
    }
  }
})

// The address bar is the one source of which session is open: every entry
// point navigates, and this watch applies the result.
watch(() => route.query.session, (value) => {
  const sid = String(value ?? '').trim()
  if (sid && sid !== sessionId.value) switchToSession(sid)
}, { immediate: true })

watch(sessionId, (sid) => {
  void loadSessionInfo(sid)
}, { immediate: true })

// Every refresh of the shared list (a turn's end, the drawer opening, a
// rename) re-reads the open conversation's own facts, so a title the
// gateway just wrote shows up without leaving the page.
watch(sessions, () => {
  void loadSessionInfo(sessionId.value)
})

// Keep the address bar honest when the session changes from inside the view
// (a slash command switching sessions, a roster jump).
watch(sessionId, (sid) => {
  // While a send is streaming, the address bar leads: a send into the
  // session the drawer just opened would otherwise be dragged back to the
  // previous session's URL here.
  if (sid && !isStreaming.value && route.query.session !== sid) {
    void router.replace({ query: { ...route.query, session: sid } })
  }
})



onUnmounted(() => {
  window.removeEventListener('pointerdown', handleWindowPointerDown)
  workbenchMq?.removeEventListener('change', syncWorkbenchOverlay)
})
</script>

<style scoped>
/* Switching between the conversation and a subagent's view is a change of
   channel, so it crossfades instead of cutting. */
.forebrain-agent-view-enter-active,
.forebrain-agent-view-leave-active {
  transition: opacity 140ms ease, transform 140ms ease;
}

.forebrain-agent-view-enter-from,
.forebrain-agent-view-leave-to {
  opacity: 0;
  transform: translateY(4px);
}

@media (prefers-reduced-motion: reduce) {
  .forebrain-agent-view-enter-active,
  .forebrain-agent-view-leave-active {
    transition: none;
  }
}

.typing-dots {
  display: inline-flex;
  align-items: center;
  gap: 6px;
}

.typing-dot {
  width: 6px;
  height: 6px;
  border-radius: 9999px;
  background: var(--forebrain-brand-1);
  opacity: 0.35;
  animation: typing-bounce 1.1s infinite ease-in-out;
  
}

.typing-dot:nth-child(2) {
  animation-delay: 0.15s;
}

.typing-dot:nth-child(3) {
  animation-delay: 0.3s;
}

@keyframes typing-bounce {
  0%,
  60%,
  100% {
    transform: translateY(0);
    opacity: 0.35;
  }
  30% {
    transform: translateY(-4px);
    opacity: 0.9;
  }
}

.chat-scrollbar {
  overflow: hidden;
}

:deep(.chat-scrollbar > div > div) {
  overflow-y: scroll !important;
  scrollbar-width: thin;
  scrollbar-color: var(--forebrain-brand-scrollbar) transparent;
}

:deep(.chat-scrollbar > div > div::-webkit-scrollbar) {
  width: 10px;
}

:deep(.chat-scrollbar > div > div::-webkit-scrollbar-thumb) {
  background: var(--forebrain-brand-scrollbar-thumb);
  border-radius: 999px;
  border: 2px solid transparent;
  background-clip: content-box;
}

:deep(.chat-scrollbar > div > div::-webkit-scrollbar-track) {
  background: transparent;
}

/* A project conversation's tag: the project band's dark ground, as a chip. */
.chat-project-tag {
  flex-shrink: 0;
  border-radius: 9999px;
  padding: 2px 8px;
  font-size: 11px;
  line-height: 16px;
  background: #0f1d38;
  color: #ffffff;
}

.chat-project-tag:hover {
  text-decoration: underline;
}

</style>
