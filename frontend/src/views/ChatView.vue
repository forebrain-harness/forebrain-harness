<template>
  <!-- The shell owns the viewport height; the thread just fills what is left
       of it, so the composer stays pinned without guessing the chrome's size. -->
  <div class="chat-shell flex h-full min-h-0 w-full flex-1 overflow-hidden">
    <div class="relative flex min-h-0 min-w-0 flex-1 flex-row overflow-hidden bg-[var(--forebrain-surface)]">
      <main
        class="flex h-full min-h-0 w-full flex-1 flex-col overflow-hidden"
        :class="{ 'chat-col--fresh': isFreshChat }"
      >
        <div class="min-h-0 flex-1 overflow-hidden">
          <!-- The new-conversation page: the greeting above the composer, and
               both centred in the pane with the quick starts under them
               (prototype .nc) — nothing above the composer to clip what the
               project pill's chooser opens. -->
          <div v-if="isFreshChat" data-testid="chat-greeting">
            <h2 class="chat-greeting">{{ t('chat.greeting') }}</h2>
          </div>
          <!-- The thread is the conversation's own: a subagent's work is read
               in the drawer beside it, never in place of it. -->
          <Conversation
            v-else
            ref="conversationRef"
            class="relative h-full overscroll-contain"
            :aria-label="t('chat.threadAria')"
            @scroll.passive="recordCurrentBrowseState"
          >
            <ConversationContent class="chat-thread-col gap-4 px-0 pt-6 pb-2">
            <!-- A new conversation whose first message created its project
                 says so once, at the top of the thread. -->
            <div v-if="createdLine" class="sys-line" data-testid="created-project-line">
              <FolderPlus class="size-4" aria-hidden="true" />
              {{ t('chat.createdProject', { name: createdLine.name }) }}
            </div>
            <Alert v-if="historyError" variant="destructive" class="mb-4">
              <CircleAlert class="size-4" />
              <AlertTitle>{{ t('chat.historyLoadFailed') }}</AlertTitle>
              <AlertDescription class="space-y-2">
                <div class="whitespace-pre-line">{{ historyError }}</div>
                <Button
                  type="button"
                  class="forebrain-btn forebrain-btn-ghost h-auto text-xs"
                  :disabled="historyLoading || !sessionId"
                  @click="sessionId && loadMessages(sessionId)"
                >{{ t('chat.retry') }}</Button>
              </AlertDescription>
            </Alert>
            <!--
              The MCP startup, while it is happening. It is live state pushed on its
              own socket op, and it is the answer to "why is the first turn waiting":
              the terminal shows the same line, so both surfaces say the same thing
              about the same runner.
            -->
            <div
              v-if="mcpStartupLabel"
              class="mb-4 flex items-center gap-3 text-[11px] uppercase tracking-[0.28em] text-[var(--forebrain-muted-text)]"
              aria-live="polite"
              data-field="mcp-startup"
            >
              <span class="h-px flex-1 bg-[var(--forebrain-rule-line)]" />
              <span class="min-w-0 text-center font-mono leading-relaxed">{{ mcpStartupLabel }}</span>
              <span class="h-px flex-1 bg-[var(--forebrain-rule-line)]" />
            </div>
            <!-- A conversation whose directory is gone can only be read: the
                 notice sits at the head of its thread and the composer takes
                 nothing until the directory is back. -->
            <div v-if="missingDir" class="forebrain-err-block mb-4" role="alert" data-testid="dir-missing-notice">
              <CircleAlert class="size-3.5" aria-hidden="true" />
              <div>
                {{ t('chat.dirMissingLine', { dir: missingDir }) }}
                <span class="forebrain-err-s">{{ t('chat.dirMissingRecover') }}</span>
              </div>
            </div>
            <McpNotice :failures="mcpFailures" />
            <template v-if="messages.length || isStreaming">
              <div class="space-y-4">
                <template v-for="(msg, idx) in messages" :key="msg.id ?? idx">
                  <!-- A message the session model could not take: it stays in
                       the thread and only the request was withheld, so the
                       notice under it names the model and points at the way
                       out it shares with the terminal — /model. -->
                  <div
                    v-if="msg.role === 'notice' && msg.imageRefusal"
                    class="forebrain-err-block"
                    role="alert"
                    data-testid="image-refused-notice"
                    :data-image-refusal-kind="imageRefusalKind(msg)"
                  >
                    <ImageOff class="size-3.5" aria-hidden="true" />
                    <div>
                      <b>{{ msg.imageRefusal.model || sessionModelData?.model || '' }}</b> {{ t(imageRefusalSentence(msg)) }}
                      <span class="forebrain-err-s">
                        {{ t('chat.imageRefusedRecover') }}
                        <button type="button" class="rewind-undo" data-testid="image-refused-switch" @click="modelChipRef?.openMenu()">{{ t('chat.imageRefusedSwitch') }}</button>
                      </span>
                    </div>
                  </div>
                  <!-- A slash command's reply: UI output only, never part of the
                       transcript, so it is a notice card, not a message. -->
                  <div
                    v-else-if="msg.role === 'notice'"
                    class="mx-auto max-w-3xl rounded-lg border border-border bg-card px-4 py-3 text-sm text-card-foreground whitespace-pre-wrap break-words"
                  ><template v-if="msg.content">{{ msg.content }}</template><SlashPickerCard
                      v-if="msg.picker"
                      :class="msg.content ? 'mt-3 whitespace-normal' : 'whitespace-normal'"
                      :picker="msg.picker"
                      :picked="msg.picked"
                      @choose="(value) => pickFromNotice(msg, value)"
                    /></div>
                  <!-- v-if outside the loop: neither the role nor the reading
                       mode changes per plan block, and Vue gives v-if priority
                       over v-for on the same element, which reads as if it
                       did. Concise hides the run's checklist: its progress
                       still shows in the status line above the composer. -->
                  <template v-if="msg.role === 'assistant' && viewMode === 'default'">
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
                  <Message v-if="msg.role !== 'notice' && messageHasBubble(msg)" :from="messageBubbleRole(msg.role)" class="max-w-full overflow-visible">
                    <MessageContent :class="messageContentClass(msg.role)">
                      <!-- What the turn said and did, in the order it happened,
                           on one activity line (owner 2026-10-25): a turn's
                           action cards hang on one vertical line, each on a
                           node that says what it is — the brand mark for the
                           agent's own words, the brain for a thought, the
                           checklist for the todo card, the robot for a
                           subagent, a call's state for a call. Anything else
                           (a compaction, a goal line, an error) breaks the
                           line. -->
                      <div v-if="hasVisibleBlocks(msg.blocks) && drawnBlocks(msg.blocks).length" class="msg-blocks">
                        <template v-for="(run, runIdx) in activityRunsOf(msg)" :key="`${msg.id ?? idx}-run-${runIdx}`">
                          <ActivityLine v-if="run.line">
                            <!-- One checklist card per turn, holding what the
                                 list looks like now: the earlier updates are
                                 history, and history is the workbench's
                                 business (prototype :1272). It opens the line:
                                 the todo the turn works through comes first. -->
                            <ActivityStep v-if="runIdx === firstLineRunIndexOf(msg) && planUpdateOf(msg)" node="todo">
                              <PlanUpdateCard :plan="planUpdateOf(msg)!" />
                            </ActivityStep>
                            <template v-for="(block, blockIdx) in run.items" :key="timelineBlockKey(block, blockIdx)">
                              <!-- The prototype's .think: a quiet row, the
                                   same card the subagent tab and the workshop
                                   draw, so a block reads the same wherever it
                                   is shown. -->
                              <ActivityStep v-if="block.kind === 'thinking'" node="think">
                                <ThinkingCard :block="block" />
                              </ActivityStep>
                              <!-- The agent's own words hang on the brand mark,
                                   a size up from the cards (14px / 1.75). -->
                              <ActivityStep v-else-if="block.kind === 'assistant'" node="say" class="al-say">
                                <MessageResponse :content="block.text" />
                              </ActivityStep>
                              <!-- The node names the call's state (a clock while
                                   it waits on the reader); the head already
                                   carries the words, so the node never repeats
                                   them. -->
                              <ActivityStep
                                v-else-if="block.kind === 'tool' && !block.step.subagentCall"
                                :node="{ status: statusOf(block.step) }"
                              >
                                <ToolCallCard
                                  :step="block.step"
                                  :scope="workbenchScope"
                                  :default-open="false"
                                  :pending-action="gatePendingOf(msg, block.step.stepId)"
                                  @open-doc="openDocTab"
                                  @open-agent="openSubagentTab"
                                />
                              </ActivityStep>
                              <!-- A subagent_* call is one card: the call's own
                                   facts and the tasks it dispatched, checked,
                                   waited for or stopped — its prompt and its
                                   result JSON stay out of the conversation. -->
                              <ActivityStep v-else-if="block.kind === 'tool'" node="subagent">
                                <SubagentCallCard
                                  :step="block.step"
                                  :live="subagentCallTasks.get(block.step.stepId)"
                                  :now="subagentNow"
                                  :active-agent-id="drawerVisible ? shownActive : ''"
                                  @open="openSubagentTab"
                                />
                              </ActivityStep>
                              <!-- An execution dispatched with no call of its own
                                   — a plan review, a runtime dispatch — is the
                                   same card, keyed by its execution. -->
                              <ActivityStep v-else-if="block.kind === 'subagent'" node="subagent">
                                <SubagentCallCard
                                  :step="block.step"
                                  :live="subagentCallTasks.get(block.stepId)"
                                  :now="subagentNow"
                                  :active-agent-id="drawerVisible ? shownActive : ''"
                                  @open="openSubagentTab"
                                />
                              </ActivityStep>
                              <!-- A settled approval is one line on the line,
                                   with no node of its own. -->
                              <ActivityStep v-else-if="block.kind === 'approval'" :node="null">
                                <ApprovalCard :block="block" />
                              </ActivityStep>
                            </template>
                          </ActivityLine>
                          <!-- Off the line: the cards the lifecycle owns, each
                               in its own place. -->
                          <CompactionCard
                            v-else-if="run.item.kind === 'compaction'"
                            :data="run.item.compaction"
                          />
                          <GoalLine
                            v-else-if="run.item.kind === 'goal'"
                            :goal="run.item.goal"
                            @open-check="openSubagentTab"
                          />
                          <RunErrorBlock
                            v-else-if="run.item.kind === 'error'"
                            :text="run.item.text"
                            :detail="run.item.detail"
                          />
                        </template>
                        <!-- A turn that only updated the todo still draws its
                             one-card line. -->
                        <ActivityLine v-if="firstLineRunIndexOf(msg) < 0 && planUpdateOf(msg)">
                          <ActivityStep node="todo">
                            <PlanUpdateCard :plan="planUpdateOf(msg)!" />
                          </ActivityStep>
                        </ActivityLine>
                      </div>
                      <!-- What the user sent reads exactly as they wrote it, the way
                           the terminal shows it: never reinterpreted as Markdown.
                           A message the runtime sent on their behalf says who
                           sent it, above the words. -->
                      <div v-else-if="msg.role === 'user' && (String(msg.content ?? '').trim() || msg.attachments?.length)" class="u-wrap">
                        <!-- Who sent it, when it was not the reader: the engine's
                             heartbeat or a schedule. It sits beside the bubble,
                             not inside it — the bubble holds the words alone
                             (prototype :3016). -->
                        <p v-if="originLabel(msg.origin)" data-testid="message-origin"
                          class="u-origin">{{ originLabel(msg.origin) }}</p>
                        <div class="u">
                          <div v-if="String(msg.content ?? '').trim()" class="whitespace-pre-wrap break-words">{{ msg.content }}</div>
                          <!-- The prototype's .u-atts: what the message attached sits in
                               the bubble, under the words, as the terminal shows it —
                               each chip downloads by its name and previews by its mark. -->
                          <UserAttachments
                            v-if="msg.attachments?.length"
                            :attachments="msg.attachments"
                            :session-id="sessionId"
                            :row-id="msg.rowId"
                          />
                        </div>
                        <!-- The prototype's .u-acts: on hover (or keyboard focus) the
                             two actions the message offers — rewind to here and copy. -->
                        <div class="u-acts">
                          <button
                            type="button"
                            class="icon-btn"
                            v-tip:top="isStreaming ? t('chat.rewindBusy') : t('chat.rewind')"
                            :aria-label="t('chat.rewindAria')"
                            :disabled="isStreaming"
                            data-testid="message-rewind"
                            @click="openRewind(msg)"
                          >
                            <History class="size-3.5" aria-hidden="true" />
                          </button>
                          <button
                            type="button"
                            class="icon-btn"
                            v-tip:top="t('chat.copyMessage')"
                            :aria-label="t('chat.copyMessage')"
                            data-testid="message-copy"
                            @click="copyMessage(msg)"
                          >
                            <Copy class="size-3.5" aria-hidden="true" />
                          </button>
                        </div>
                      </div>
                      <MessageResponse v-else-if="String(msg.content ?? '').trim()" :key="`msg-content-${msg.id ?? idx}`" :content="msg.content" />
                      <div
                        v-if="msg.role === 'assistant' && msg.turnDiffs?.length"
                        class="mt-3 rounded-lg border border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-3"
                      >
                        <DiffView
                          :files="msg.turnDiffs"
                          :summary="msg.turnDiffs[0]?.summary"
                          :total-lines="msg.turnDiffs.reduce((s, d) => s + (d.added ?? 0) + (d.deleted ?? 0), 0)"
                        />
                      </div>
                      <!-- The run's closing line, off the line's end but in
                           its column (prototype .actline .a > .worked). -->
                      <RunWorkedLine class="ml-8" :line="workedLineOf(msg, t)" />
                    </MessageContent>
                  </Message>
                </template>
              </div>
              <Alert v-if="error" variant="destructive" class="mt-2">
                <CircleAlert class="size-4" />
                <AlertTitle>{{ t('chat.requestFailed') }}</AlertTitle>
                <AlertDescription class="whitespace-pre-line">{{ error }}</AlertDescription>
              </Alert>
            </template>
            <!-- The undo line the prototype's .sys-line draws: after a rewind it
                 says what happened and offers to put it all back, until a new
                 message fixes the rewind. It sits outside the empty/messages
                 split, because a rewind can leave the thread with nothing to
                 show and the undo must outlive that. -->
            <div v-if="rewindLine" class="sys-line rewind-line" data-testid="rewind-undo-line" style="color: var(--forebrain-text-2)">
              <History class="size-3.5" aria-hidden="true" />
              <span>{{ rewindLine.text }}</span>
              <button type="button" class="rewind-undo" data-testid="rewind-undo" @click="undoRewind">{{ t('chat.rewindUndo') }}</button>
            </div>
            <!-- What the thread's own tail still holds (plan 138): the agent's
                 questions, and the requests no tool card of this conversation
                 carries — an old session's rows, a gate with no card — in the
                 waiting card's own frame. The gates that belong to calls hang
                 on their cards above; the plan approval waits where the input
                 is. -->
            <ApprovalCard
              :anchored-action-ids="anchoredActionIds"
              can-open-agent
              @open-agent="openSubagentTab"
              @focus-composer="focusComposer"
            />
            <!-- A send the runtime refused because this conversation is
                 already running somewhere else: the words wait in the
                 composer, whole, and the thread says why nothing went. -->
            <div v-if="busyNotice" class="forebrain-err-block mt-4" role="alert" data-testid="busy-notice">
              <CircleAlert class="size-3.5" aria-hidden="true" />
              <div>
                {{ busyNoticeText }}
                <span class="forebrain-err-s">{{ t('chat.messageBackInComposer') }}</span>
              </div>
            </div>
            </ConversationContent>
            <ConversationScrollButton :label="t('chat.scrollToLatest')" />
          </Conversation>
        </div>


        <div class="chat-composer z-10 shrink-0 bg-[var(--forebrain-surface)]">
          <!-- The run's own state, where the terminal draws it: above the
               queue preview and the composer, gone the moment the run ends. -->
          <RunStatusLine
            v-if="runLine && !isFreshChat"
            data-testid="run-status-line"
            class="mb-1.5"
            :label="runLine.label"
            :reason="runLine.reason"
            :plan="runLine.plan"
            :tail="runLine.tail"
          />
          <!-- The plan approval takes the input's place while the planner
               waits (plan 138, as the terminal's overlay takes its composer):
               no card in the thread, the plan and its verdicts here, the input
               — draft, attachments, queue — hidden whole underneath, so it
               returns exactly as it was left. -->
          <PlanApprovalDock
            v-if="exitPlanPending"
            :action="exitPlanPending"
            :session-id="sessionId ?? ''"
            :on-open-plan="openPlanTab"
          />
          <!-- The provider's own root is a bare slot, so the hide that keeps
               the composer's state alive sits on a real element around it. -->
          <div v-show="!exitPlanPending" class="contents">
            <PromptInputProvider
              :max-files="composerMaxFiles"
              accept="image/*,application/pdf"
              @submit="handleSubmit"
              @error="handleComposerError"
            >
            <Alert v-if="composerNotice" variant="destructive" class="mb-2">
              <CircleAlert class="size-4" />
              <AlertTitle>{{ composerNotice.title }}</AlertTitle>
              <AlertDescription class="whitespace-pre-line">{{ composerNoticeDetail }}</AlertDescription>
            </Alert>
            <AutoContinueBanner :state="autoContinueForView('')" @cancel="cancelAutoContinue('')" />
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
                :preview="pendingInputPreview"
                @edit-last-queued="restoreLastQueuedMessage"
                @interrupt-run="interruptAndSendPendingSteers"
              />
              <!-- The prototype's .composer (:500): a 12px corner, one hairline,
                   no shadow — and the project pill drawn inside it, at the top,
                   the way .ctop is drawn inside the box. -->
              <PromptInput
                multiple
                global-drop
                class="w-full [&_[data-slot=input-group]]:rounded-xl [&_[data-slot=input-group]]:border-[var(--forebrain-divider-strong)] [&_[data-slot=input-group]]:bg-[var(--forebrain-surface)] [&_[data-slot=input-group]]:shadow-none [&_[data-slot=input-group]]:ring-0 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:border-[var(--forebrain-brand-1)] [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:border-[var(--forebrain-brand-1)] [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:ring-2 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:ring-2 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus]:ring-[var(--forebrain-brand-1)]/20 [&_[data-slot=input-group]]:has-[[data-slot=input-group-control]:focus-visible]:ring-[var(--forebrain-brand-1)]/20"
                :class="composerLocked ? 'composer-locked' : ''"
              >
                <PromptInputHeader v-if="isFreshChat" class="composer-ctop">
                  <ProjectPill
                    ref="pillRef"
                    :projects="projects"
                    :selected="target"
                    :invalid="pillInvalid"
                    @select="pickTarget"
                    @browse="openFolderDialog"
                  />
                </PromptInputHeader>
                <PromptInputBody>
                  <ForebrainPromptTextarea ref="promptRef" :placeholder="composerPlaceholder" :bots="availableBots"
                    :bots-fetch-done="botsFetchDone" :during-run="isStreaming" :queue-visible="composerQueueVisible"
                    :disabled="composerLocked" :scope="workbenchScope" attach-save
                    @files-picked="onFilesPicked"
                    @queue-follow-up="markNextSubmissionAsQueued"
                    @edit-last-queued="restoreLastQueuedMessage"
                    @interrupt-run="interruptAndSendPendingSteers"
                    @typed="cancelAutoContinue('')"
                    @escape="cancelAutoContinue('')" />
                </PromptInputBody>
                <PromptInputFooter class="!px-2 !pb-2">
                  <!-- The composer's own bar, in the prototype's order (:503,
                       gap 8px): on the left the "+", the mode and the approval
                       preset; on the right the context ring, the model, the
                       reasoning effort, then send. Every chip is drawn whether
                       or not its data has landed yet, so the bar keeps its
                       shape from the first frame. -->
                  <div class="flex min-w-0 flex-1 items-center gap-2">
                    <ComposerPlusMenu :disabled="composerLocked" @insert="insertPlusCommand" />
                    <ModeChip :mode="mode" :disabled="isStreaming || composerLocked" @select="selectMode" />
                    <ApprovalPresetPicker ref="approvalPickerRef" :session-id="sessionId" />
                    <ViewModeChip v-model="viewMode" />
                  </div>
                  <div class="flex items-center gap-2">
                    <ContextRing
                      v-if="contextUsedPercent != null"
                      :percent="contextUsedPercent"
                      @open="workbench.openOn('context')"
                    />
                    <ModelChip
                      ref="modelChipRef"
                      :session-id="sessionId ?? ''"
                      :selection="sessionModelData"
                      :options="sessionModelData?.options ?? []"
                      :disabled="composerLocked || !sessionModelData"
                      :compact="drawerVisible"
                      @saved="loadSessionModel(sessionId ?? '')"
                      @pick="pickDraftModel"
                    />
                    <EffortChip
                      :effort="sessionModelData?.effort ?? ''"
                      :efforts="sessionEfforts"
                      :disabled="composerLocked || !sessionModelData"
                      @select="selectEffort"
                    />
                    <PromptInputSubmit
                      :status="submitStatus"
                      :disabled="composerLocked"
                      :label="submitLabel"
                      @stop="interruptAndSendPendingSteers"
                    />
                  </div>
                </PromptInputFooter>
              </PromptInput>
            </div>
            </PromptInputProvider>
          </div>
          <!-- The quick starts, under the composer, exactly as the new-chat
               page's prototype draws them. -->
          <div v-if="isFreshChat" class="starters" data-testid="chat-starters">
            <button
              v-for="starter in starters"
              :key="starter.action"
              type="button"
              class="starter"
              data-testid="chat-starter"
              @click="runStarter(starter.action)"
            >
              <component :is="starter.icon" class="size-3.5" aria-hidden="true" />
              {{ t(starter.label) }}
            </button>
          </div>
        </div>
      </main>
      <!-- The subagents this conversation dispatched, beside it: one tab each,
           mounted for as long as the tab is open so a draft, an attachment or
           a scroll position stays with the subagent it belongs to. -->
      <SubagentDrawer
        v-if="drawerVisible"
        :tabs="drawerRecords"
        :active="shownActive"
        :records="conversationSubagents"
        :seen="seenAgentActivity"
        :overlay="workbenchOverlay"
        :now="subagentNow"
        :plan-tabs="planTabs"
        :active-plan="planActive"
        :doc-tabs="docTabs"
        :active-doc="docActive"
        :order="drawerTabOrder"
        @select="selectSubagentTab"
        @close-tab="closeSubagentTab"
        @open="openSubagentTab"
        @collapse="collapseSubagentDrawer"
        @escape-key="planActive || docActive ? collapseSubagentDrawer() : handleSubagentEscape(shownActive)"
        @select-plan="selectPlanTab"
        @close-plan="closePlanTab"
        @select-doc="selectDocTab"
        @close-doc="closeDocTab"
      >
        <!-- The open plan documents: their own tabs beside the subagents', and
             the subagents stay mounted underneath so a draft, an attachment or
             a scroll position survives a look at the plan. -->
        <PlanDoc
          v-for="tab in planTabs"
          v-show="tab.id === planActive"
          :key="tab.id"
          :path="tab.path"
          :at="tab.at"
          :done="tab.done"
          :total="tab.total"
          :markdown="tab.markdown"
        />
        <!-- The documents a card head opened — a whole command, a whole title,
             a whole output — in the same drawer, beside everything else. -->
        <template v-for="tab in docTabs">
          <ToolDocCommand
            v-if="tab.kind === 'command'"
            v-show="tab.id === docActive && !planActive"
            :key="tab.id"
            :command="tab.command ?? tab.text"
          />
          <ToolDocOutput
            v-else-if="tab.kind === 'output' && outputSourceOf(tab)"
            v-show="tab.id === docActive && !planActive"
            :key="tab.id"
            :source="outputSourceOf(tab)!"
          />
          <ToolDocTitle
            v-else-if="tab.kind === 'title'"
            v-show="tab.id === docActive && !planActive"
            :key="tab.id"
            :text="tab.text"
          />
        </template>
        <SubagentTab
          v-for="record in drawerRecords"
          v-show="!planActive && !docActive && record.agentId === shownActive"
          :key="record.agentId"
          :ref="(el) => setSubagentTabRef(record.agentId, el)"
          :record="record"
          :fold-state="browseStateFor(record.agentId).folds"
          :call-tasks="subagentCallTasks"
          :now="subagentNow"
          :scope="workbenchScope"
          :active-agent-id="shownActive"
          :pending-input="subagentPendingInput[record.agentId] ?? emptyPendingInputPreview()"
          :auto-continue="autoContinueForView(record.agentId)"
          :notice="subagentNotices[record.agentId] ?? null"
          :context="subagentContextTarget === record.agentId ? { data: subagentContextDebug, loading: subagentContextLoading } : null"
          @submit="(m) => handleSubagentSubmit(record.agentId, m)"
          @fold-change="(v) => updateAgentFold(record.agentId, v)"
          @open-agent="openSubagentTab"
          @open-doc="openDocTab"
          @stop="stopSubagent(record.agentId)"
          @edit-last-queued="recallIntoTab(record.agentId)"
          @interrupt-run="interruptSubagentToSend(record.agentId)"
          @typed="cancelAutoContinue(record.agentId)"
          @escape-key="handleSubagentEscape(record.agentId)"
          @files-picked="(files) => onFilesPicked(files, record.agentId)"
          @cancel-auto-continue="cancelAutoContinue(record.agentId)"
          @refresh-context="openSubagentContext(record.agentId)"
          @dismiss-notice="subagentNotices[record.agentId] = null"
        />
      </SubagentDrawer>
      <ChatWorkbench
        v-if="workbench.open.value && !isFreshChat && !drawerVisible"
        :records="runningRoster"
        :loading="rosterLoading"
        :error="rosterError"
        :overlay="workbenchOverlay"
        :scope="workbenchScope"
        :root-path="sessionProject?.root ?? ''"
        :requested-tab="workbench.requested.value"
        @requested-tab-shown="workbench.consumed()"
        @close="workbench.close()"
        @view="viewRoster"
        @cancel="cancelRoster"
        @cancel-all="cancelAll"
        @insert-ref="insertWorkspaceRef"
        @open-plan="openPlanTab"
      />
      <FolderDialog
        v-if="folderOpen"
        :projects="projects"
        :initial-path="target?.root"
        @select="onFolderPick"
        @cancel="folderOpen = false"
      />
      <TrustDialog
        v-if="trustOpen && trustTarget"
        :target="trustTarget"
        @confirm="confirmTrust"
        @cancel="cancelTrust"
      />
      <!-- The project's MCP and language-server entries nobody has confirmed:
           a session re-resolves what it would load, and an entry the project
           declares waits for one answer on one panel before it can run. -->
      <ProjectServersConsentDialog
        :open="projectServersOpen"
        :mcp="projectServersMcp"
        :lsp="projectServersLsp"
        :busy="projectServersBusy"
        :error="projectServersError"
        @confirm="confirmProjectServers"
        @decline-all="declineAllProjectServers"
        @close="declineAllProjectServers"
      />
      <RewindDialog
        v-if="rewindTarget"
        :message="rewindTarget.content"
        :preview="rewindPreview"
        :busy="rewindBusy"
        :error="rewindError"
        @restore="applyRewind"
        @cancel="closeRewind"
      />
      <AttachSaveDialog
        :open="attachOpen"
        :project="attachProject"
        :files="attachRows"
        :initial-dir="attachDir"
        @confirm="confirmAttach"
        @cancel="closeAttach"
      />
    </div>
  </div>
</template>

<script setup lang="ts">
import { h, ref, computed, nextTick, onMounted, onUnmounted, watch, withDirectives } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { CheckCircle, CircleAlert, Bot, ImageOff, PanelRight, FolderGit2, FolderPlus, FileText, Map, Hammer, Clock, History, Copy } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { Button } from '@repo/shadcn-vue/components/ui/button'
import ProjectPill from '@/components/chat/ProjectPill.vue'
import FolderDialog from '@/components/chat/FolderDialog.vue'
import RewindDialog from '@/components/chat/RewindDialog.vue'
import TrustDialog from '@/components/chat/TrustDialog.vue'
import ProjectServersConsentDialog from '@/components/chat/ProjectServersConsentDialog.vue'
import SessionHeartbeat from '@/components/chat/SessionHeartbeat.vue'
import SubagentCallCard from '@/components/chat/SubagentCallCard.vue'
import SubagentDrawer from '@/components/chat/SubagentDrawer.vue'
import SubagentTab from '@/components/chat/SubagentTab.vue'
import RunStatusLine from '@/components/chat/RunStatusLine.vue'
import SlashPickerCard from '@/components/chat/SlashPickerCard.vue'
import ActivityLine from '@/components/chat/ActivityLine.vue'
import ActivityStep from '@/components/chat/ActivityStep.vue'
import PlanUpdateCard from '@/components/chat/PlanUpdateCard.vue'
import ToolCallCard, { statusOf } from '@/components/chat/ToolCallCard.vue'
import ToolDocCommand from '@/components/chat/ToolDocCommand.vue'
import ToolDocOutput from '@/components/chat/ToolDocOutput.vue'
import ToolDocTitle from '@/components/chat/ToolDocTitle.vue'
import ThinkingCard from '@/components/chat/ThinkingCard.vue'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import PlanApprovalDock from '@/components/chat/PlanApprovalDock.vue'
import McpNotice from '@/components/chat/McpNotice.vue'
import ComposerPlusMenu from '@/components/chat/ComposerPlusMenu.vue'
import ModeChip from '@/components/chat/ModeChip.vue'
import UserAttachments from '@/components/chat/UserAttachments.vue'
import ModelChip from '@/components/chat/ModelChip.vue'
import EffortChip from '@/components/chat/EffortChip.vue'
import ContextRing from '@/components/chat/ContextRing.vue'
import {
  Conversation,
  ConversationContent,
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
  PromptInputBody,
  PromptInputFooter,
  PromptInputHeader,
  PromptInputProvider,
  PromptInputSubmit,
} from '@repo/elements/prompt-input'
import ForebrainPromptTextarea, { type BotOption } from '@/components/ForebrainPromptTextarea.vue'
import ChatWorkbench from '@/components/chat/ChatWorkbench.vue'
import ApprovalPresetPicker from '@/components/chat/ApprovalPresetPicker.vue'
import ViewModeChip from '@/components/chat/ViewModeChip.vue'
import RunWorkedLine from '@/components/chat/RunWorkedLine.vue'
import RunErrorBlock from '@/components/chat/RunErrorBlock.vue'
import PendingInputQueuePopover from '@/components/PendingInputQueuePopover.vue'
import AutoContinueBanner from '@/components/chat/AutoContinueBanner.vue'
import LspRecommendationCard from '@/components/chat/LspRecommendationCard.vue'
import DiffView from '@/components/DiffView.vue'
import CompactionCard from '@/components/chat/CompactionCard.vue'
import GoalLine from '@/components/chat/GoalLine.vue'
import { FOREBRAIN_GOAL_CHECK_AGENT_TYPE } from '@/lib/forebrainGatewayRuntime'
import { agentRosterConversationTarget, useAgentRoster } from '@/composables/useAgentRoster'
import { useChatStream, workedLineOf, emptyPendingInputPreview, hasVisibleBlocks, timelineBlockKey, formatFirstTokenDuration, formatRuntimeStatusLabel, formatTransportRetryLine, type ChatMessage, type PlanBlock, type SubagentTranscript, type TimelineBlock } from '@/composables/useChatStream'
import { usePendingActions } from '@/composables/usePendingActions'
import { providePendingActions } from '@/composables/providePendingActions'
import type { DocTab } from '@/composables/useDocTabs'
import { blockOnActivityLine, groupActivityRuns } from '@/lib/activityRuns'
import { subagentPhase } from '@/lib/subagentStatus'
import { blockVisibleIn } from '@/lib/viewMode'
import { useViewMode } from '@/composables/useViewMode'
import { type PlanTab, type SessionPlan } from '@/composables/useSessionPlans'
import { planProgress, planTitle } from '@/lib/planProgress'
import { formatClock } from '@/lib/time'
import PlanDoc from '@/components/chat/PlanDoc.vue'
import { useLastSession } from '@/composables/useLastSession'
import { useTopbar } from '@/composables/useTopbar'
import { useWorkbench } from '@/composables/useWorkbench'
import { useWorkspaceTree, migrateWorkspaceTree } from '@/composables/useWorkspaceTree'
import { useChatSessions } from '@/composables/useChatSessions'
import { useSessionInfo, sessionHeaderTitle } from '@/composables/useSessionInfo'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { getErrorMessage, forebrainApi, type AgentRosterRow, type ProjectRecord, type ProjectServerPendingRow, type RewindPreview, type SessionContextDebug, type SessionModelResponse, type SlashCommandRecord, type WorkspaceScope } from '@/lib/api'
import { targetFromFolder, targetFromProject, type ProjectTarget } from '@/lib/projectTarget'
import { formatProviderError } from '@/lib/providerError'
import type { ComposerSubmission, PickedFile, SubmittedAttachment } from '@/lib/composerSubmission'
import AttachSaveDialog, { type AttachFileRow } from '@/components/chat/AttachSaveDialog.vue'
import tip from '@/directives/tip'
import { sessionTitleFromContent } from '@/lib/sessionTitle'
import { useI18n } from '@/locales'
import type { I18nKey } from '@/locales'
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

/** The words of the thread's first user message: what the engine names a
 *  session after, and therefore what the top bar calls it until it has. */
const firstUserText = computed(() => {
  const first = messages.value.find((message) => message.role === 'user')
  return first ? String(first.content ?? '') : ''
})

/** The conversation's name, read from the session itself. A session the server
 *  has not titled yet is named by what the engine will name it after: its first
 *  user message, first line, at most 50 characters (plan 072). */
const sessionTitle = computed(() => {
  const info = sessionInfo.value
  // A title equal to the session id is the placeholder a fresh row carries.
  const titled = info && info.title && info.title !== sessionId.value ? info : null
  const temporary = sessionTitleFromContent(firstUserText.value)
  return sessionHeaderTitle(titled, temporary || t('nav.chat'))
})
const { t, locale } = useI18n()
const router = useRouter()

// ---- The conversation's row in the shell's top bar (plan 016 step 1) ----

const { set: setTopbar } = useTopbar()

/**
 * A refusal that says this conversation is already running a turn: it is the
 * thread's to say, not the composer's — the words wait in the composer whole
 * while the notice names why nothing went. The code is kept, so the sentence
 * follows the viewer's language for as long as it is on screen.
 */
// The code the runtime sent, not a guessed one: the same refusal has two
// sentences — running here, running on another surface — and only the runtime
// knows which one happened (prototype :3854).
const busyNotice = ref('')
const busyNoticeText = computed(() => busyNotice.value
  ? (formatProviderError({ code: busyNotice.value }, locale.value) ?? '')
  : '')

// The factories run inside the shell's render, so the row tracks this
// conversation's own state — title, project, heartbeat, roster — without the
// page re-registering anything.
setTopbar({
  // A new conversation names nothing yet: its top bar starts empty, and the
  // workbench waits for the first message (owner 2026-10-18).
  leading: () => (isFreshChat.value ? [] : [
    withDirectives(h('span', {
      class: 'forebrain-topbar-title',
      'data-testid': 'chat-session-title',
    }, sessionTitle.value), [[tip, sessionTitle.value, 'bottom', { trunc: true }]]),
  // A project's conversation says whose it is and leads back to that
  // project's space, where its sessions are listed.
  ...(sessionProject.value ? [(() => {
    const project = sessionProject.value!
    return withDirectives(h(RouterLink, {
      to: `/projects/${encodeURIComponent(project.id)}/overview`,
      class: 'chat-project-tag',
      'data-testid': 'chat-project-tag',
    }, { default: () => [h(FolderGit2, { 'aria-hidden': 'true' }), project.name] }), [[tip, t('chat.openProjectSpace'), 'bottom']])
  })()] : []),
  ]),
  trailing: () => [
    // The heartbeat is this conversation's, so its control sits in the
    // conversation's row; without a conversation there is nothing for it to
    // ask again.
    ...(sessionId.value ? [h(SessionHeartbeat, { sessionId: sessionId.value })] : []),
    // The subagent drawer answers for this conversation's subagents, so its
    // control appears with the first one dispatched — never before.
    ...(!isFreshChat.value && conversationSubagents.value.length ? [h('button', {
      type: 'button',
      class: 'forebrain-chip-btn',
      'aria-pressed': String(drawerVisible.value),
      'data-testid': 'subagent-drawer-toggle',
      onClick: toggleSubagentDrawer,
    }, [
      h(Bot, { class: 'size-3.5', 'aria-hidden': 'true' }),
      h('span', {}, t('chat.subagentDrawer')),
      // The badge counts what is running now, not what is open.
      ...(runningSubagentCount.value ? [h('span', { class: 'forebrain-chip-count' }, String(runningSubagentCount.value))] : []),
    ])] : []),
    // The third column belongs to a conversation: a new chat has none yet.
    ...(isFreshChat.value ? [] : [h('button', {
      type: 'button',
      class: 'forebrain-chip-btn',
      'aria-pressed': String(workbench.open.value),
      'data-testid': 'workbench-toggle',
      onClick: () => workbench.toggle(),
    }, [
      h(PanelRight, { class: 'size-3.5', 'aria-hidden': 'true' }),
      h('span', {}, t('chat.workbench')),
    ])]),
  ],
})

const promptRef = ref<InstanceType<typeof ForebrainPromptTextarea> | null>(null)
// How many files the composer takes for one message.
const composerMaxFiles = 5
const availableBots = ref<BotOption[]>([])
const botsFetchDone = ref(true)

// ---- "Save to" attach dialog (plan 030) ----

/** The files a paste, drop or pick handed up, waiting to be saved. */
const attachPending = ref<PickedFile[]>([])
const attachProject = ref<{ id: string; name: string } | null>(null)
/** The project's last attach directory the dialog opens on ('' is the root). */
const attachDir = ref('')
const attachOpen = ref(false)
/** Which composer the files being saved belong to: a subagent's tab, or the
 *  conversation's own when empty. */
const attachTarget = ref('')
/** The attach upload in flight, so a send that races it waits, not drops it. */
let pendingAttachUpload: Promise<void> | null = null

const attachRows = computed<AttachFileRow[]>(() =>
  attachPending.value.map((f) => ({
    filename: f.filename,
    size: f.file.size,
    mediaType: f.mediaType,
    fromClipboard: f.fromClipboard ?? false,
  })))

// ---- New-conversation project picker (plan 029) ----

/**
 * A brand-new conversation — no session opened yet — shows the greeting, the
 * project pill and the quick starts. A conversation that exists (even one with
 * no messages yet, opened from the project space) keeps the plain composer.
 */
const isFreshChat = computed(() =>
  !sessionId.value && !messages.value.length && !isStreaming.value)


const projects = ref<ProjectRecord[]>([])
const target = ref<ProjectTarget | null>(null)
const pillRef = ref<InstanceType<typeof ProjectPill> | null>(null)
const pillInvalid = ref(false)
const folderOpen = ref(false)
const trustOpen = ref(false)
const trustTarget = ref<ProjectTarget | null>(null)
let trustResolver: ((ok: boolean) => void) | null = null
// The project a new conversation's first message just created, shown once as
// a system line at the top of its thread.
const createdLine = ref<{ name: string } | null>(null)

// ---- Rewind to before a message (plan 033) ----

/** The message the open rewind dialog targets, and the preview it loaded. */
const rewindTarget = ref<ChatMessage | null>(null)
const rewindPreview = ref<RewindPreview | null>(null)
const rewindBusy = ref(false)
const rewindError = ref('')
/** The system line under the thread after a rewind, until a new message fixes it. */
const rewindLine = ref<{ text: string } | null>(null)

function firstLineText(text: string): string {
  const line = String(text ?? '').split('\n')[0] ?? ''
  return line.length > 60 ? `${line.slice(0, 60)}…` : line
}

/** Opens the confirm dialog for one user message, loading its file preview. */
async function openRewind(msg: ChatMessage) {
  if (isStreaming.value) return
  const sid = String(sessionId.value ?? '').trim()
  if (!sid) return
  let target = msg
  let rowId = Number(msg.rowId ?? 0)
  if (!rowId) {
    // A message sent live has no row id yet: the messages API is its source
    // (plan 033), so read the transcript back and find it there. Its ordinal
    // among the user messages is what identifies it across the reload, whose
    // ids differ from the live ones.
    const ordinal = messages.value.filter((m) => m.role === 'user').findIndex((m) => m.id === msg.id)
    await loadMessages(sid)
    const users = messages.value.filter((m) => m.role === 'user')
    const match = ordinal >= 0
      ? users[ordinal]
      : users.find((m) => m.content === msg.content)
    rowId = Number(match?.rowId ?? 0)
    if (!rowId) return
    target = match ?? msg
  }
  rewindTarget.value = target
  rewindPreview.value = null
  rewindError.value = ''
  try {
    rewindPreview.value = await forebrainApi.rewindPreview(sid, rowId)
  } catch (cause) {
    rewindError.value = getErrorMessage(cause)
  }
}

function closeRewind() {
  rewindTarget.value = null
  rewindPreview.value = null
  rewindError.value = ''
}

/** Copies one message's text, as the hover action offers. */
async function copyMessage(msg: ChatMessage) {
  try {
    await navigator.clipboard.writeText(String(msg.content ?? ''))
  } catch {
    // A denied clipboard is not worth an error line: the copy just did not happen.
  }
}

/** Runs the rewind the dialog confirmed: removes the trailing bubbles, puts the
 * anchor back in the composer, and leaves the undo line. */
async function applyRewind(payload: { mode: 'both' | 'chat' | 'code'; keepPaths: string[] }) {
  const sid = String(sessionId.value ?? '').trim()
  const rowId = Number(rewindTarget.value?.rowId ?? 0)
  if (!sid || !rowId || rewindBusy.value) return
  rewindBusy.value = true
  rewindError.value = ''
  try {
    const res = await forebrainApi.rewind(sid, { messageId: rowId, mode: payload.mode, keepPaths: payload.keepPaths })
    rewindLine.value = {
      text: payload.mode === 'code'
        ? t('chat.rewindDoneCode', { n: res.restored.length, text: firstLineText(res.anchorText) })
        : t('chat.rewindDoneBoth', { n: res.restored.length }),
    }
    promptRef.value?.restoreSubmission({ text: res.anchorText, attachments: [], mentionImages: [] })
    closeRewind()
    await loadMessages(sid)
  } catch (cause) {
    const detail = getErrorMessage(cause)
    rewindError.value = detail === 'session_busy' ? t('chat.rewindBusy') : detail
  } finally {
    rewindBusy.value = false
  }
}

/** Undoes the pending rewind: files and conversation come back. */
async function undoRewind() {
  const sid = String(sessionId.value ?? '').trim()
  if (!sid) return
  try {
    await forebrainApi.rewindUndo(sid)
    rewindLine.value = null
    await loadMessages(sid)
  } catch (cause) {
    error.value = getErrorMessage(cause)
  }
}

const starters: Array<{ icon: unknown; label: I18nKey; action: string }> = [
  { icon: FileText, label: 'chat.starterInit', action: '/init' },
  { icon: Map, label: 'chat.starterPlan', action: '@plan' },
  { icon: Hammer, label: 'chat.starterSkill', action: '@workshop' },
  { icon: Clock, label: 'chat.starterCron', action: '@cron' },
]

async function loadProjects() {
  try {
    projects.value = await forebrainApi.projectsList({ sort: 'updated' })
  } catch {
    projects.value = []
  }
}

/**
 * The project the pill preselects: the one the last-used conversation belonged
 * to, so a new chat picks up where the user left off.
 */
async function defaultTarget() {
  const lid = String(lastSessionId.value ?? '').trim()
  if (!lid) return null
  try {
    const info = await forebrainApi.chatSession(lid)
    if (!info.project) return null
    const match = projects.value.find((p) => p.id === info.project?.id)
    if (match) return targetFromProject(match)
    return { projectId: info.project.id, name: info.project.name, root: '', isNew: false, trustLevel: 'trusted' } as ProjectTarget
  } catch {
    return null
  }
}

function pickTarget(next: ProjectTarget) {
  target.value = next
  pillInvalid.value = false
}

function openFolderDialog() {
  folderOpen.value = true
}

function onFolderPick(path: string) {
  folderOpen.value = false
  const existing = projects.value.find((p) => p.root === path)
  target.value = targetFromFolder(path, existing)
  pillInvalid.value = false
}

/** A pick from the composer's "+" menu: the goal command or a skill's command
 *  goes into the composer the way a quick start does, ready for its argument. */
function insertPlusCommand(text: string) {
  promptRef.value?.restoreSubmission({ text, attachments: [], mentionImages: [] })
}

function runStarter(action: string) {  if (action === '@plan') {
    // Mode selection is web plan 016; until then the starter types the plan
    // command into the composer.
    promptRef.value?.restoreSubmission({ text: '/plan ', attachments: [], mentionImages: [] })
    return
  }
  if (action === '@workshop') {
    void router.push({ name: 'workshop' })
    return
  }
  if (action === '@cron') {
    // Cron lives in a project space (web plan 035); until then jump to the
    // project space.
    void router.push({ name: 'projects' })
    return
  }
  promptRef.value?.restoreSubmission({ text: `${action} `, attachments: [], mentionImages: [] })
}

/**
 * Ask once whether a directory nobody has judged may be trusted — the same
 * question, and the same record, as the terminal's launch prompt. Resolves
 * with the user's answer.
 */
function askTrust(t: ProjectTarget): Promise<boolean> {
  trustTarget.value = t
  trustOpen.value = true
  return new Promise((resolve) => {
    trustResolver = resolve
  })
}

async function confirmTrust() {
  const t = trustTarget.value
  trustOpen.value = false
  if (t) {
    try {
      await forebrainApi.hostTrustSet(t.root, true)
      t.trustLevel = 'trusted'
    } catch {
      // A failure to record the decision still lets the send proceed: the
      // prompt is an added gate, not the mechanism.
    }
  }
  trustResolver?.(true)
  trustResolver = null
}

function cancelTrust() {
  trustOpen.value = false
  trustResolver?.(false)
  trustResolver = null
}

/**
 * Writes the model, effort and approval preset a new chat's chips were set to
 * onto the conversation its first message just opened. A write that fails
 * leaves that conversation on the default; the chips then read what it really
 * runs on, so nothing claims a choice the server does not hold.
 */
async function applyDraftChoices(id: string) {
  const model = draftModel.value
  const preset = approvalPickerRef.value?.takeDraft() ?? null
  draftModel.value = null
  const writes: Promise<unknown>[] = []
  if (model) writes.push(forebrainApi.saveSessionModel(id, model).catch(() => undefined))
  if (preset) writes.push(forebrainApi.sessionPreset(id, preset).catch(() => undefined))
  await Promise.all(writes)
}

/**
 * Open the conversation this send belongs to: the current one, or a new one in
 * the picked project. Returns null when nothing was opened — no project picked
 * (the pill is marked) or the trust prompt was declined — so the caller keeps
 * the message in the composer.
 */
async function ensureSessionForSend(): Promise<string | null> {
  if (sessionId.value) return sessionId.value
  const picked = target.value
  if (!picked) {
    pillInvalid.value = true
    pillRef.value?.focus()
    return null
  }
  if (picked.trustLevel === 'unknown') {
    const trusted = await askTrust(picked)
    if (!trusted) return null
  }
  try {
    const created = await forebrainApi.chatSessionCreate({
      ...(picked.projectId ? { projectId: picked.projectId } : { folder: picked.folder }),
    })
    createdLine.value = created.created ? { name: created.project?.name ?? picked.name } : null
    // What the chips were set to before there was a conversation becomes this
    // conversation's own choice, ahead of its first turn (and before the page
    // re-reads the session's chips, so they show what it will really run on).
    await applyDraftChoices(created.id)
    sessionId.value = created.id
    // The tree the pill's project already loaded is this conversation's file
    // view now: carry it over so the first send does not flash an empty tree.
    migrateWorkspaceTree({ projectId: picked.projectId }, { sessionId: created.id })
    // The address bar is the one source of the open session; `new` has done
    // its job and is dropped.
    await router.replace({ query: { session: created.id } })
    return created.id
  } catch (cause) {
    const message = getErrorMessage(cause)
    if (message.includes('session_directory_missing')) {
      composerNotice.value = {
        title: t('chat.messageNotSent'),
        detail: t('chat.sessionDirMissing', { dir: picked.root }),
      }
    } else {
      composerNotice.value = { title: t('chat.messageNotSent'), detail: message }
    }
    return null
  }
}

/**
 * A file the user attached is saved into the project before the message is
 * sent. The conversation is opened first — an upload belongs to a session, and
 * a project is where it lands, so no project picked marks the pill exactly as
 * a send would — then the "save to" dialog is shown. Nothing uploads until it
 * is confirmed; a cancel simply drops the files.
 *
 * `agentId` names the composer the files came from: a subagent's tab while it
 * is open, the conversation's own composer otherwise.
 */
async function onFilesPicked(files: PickedFile[], agentId = '') {
  const allowed = files.filter((f) => isAllowedAttachmentType(f.mediaType, f.filename))
  if (!allowed.length) return
  const opened = await ensureSessionForSend()
  if (!opened) return
  const project = await attachProjectFor(opened)
  if (!project) return
  attachTarget.value = String(agentId ?? '').trim()
  attachPending.value = allowed
  attachProject.value = { id: project.id, name: project.name }
  attachDir.value = project.lastAttachDir ?? ''
  attachOpen.value = true
}

/** The project a conversation belongs to, in the shape the dialog needs. */
async function attachProjectFor(session: string): Promise<{ id: string; name: string; lastAttachDir: string } | null> {
  try {
    const info = await forebrainApi.chatSession(session)
    if (!info.project) return null
    return { id: info.project.id, name: info.project.name, lastAttachDir: info.project.lastAttachDir ?? '' }
  } catch {
    return null
  }
}

function closeAttach() {
  attachOpen.value = false
  attachPending.value = []
  attachTarget.value = ''
}

/** Save the waiting files into dir and hand them to the composer, which sends
 *  them by id with the next message. A send that lands before the upload
 *  finishes waits for it (waitForPendingAttachUpload), so an attachment
 *  confirmed and sent in quick succession is never dropped. */
async function confirmAttach(dir: string) {
  const session = sessionId.value
  const pending = attachPending.value
  if (!session || !pending.length) {
    closeAttach()
    return
  }
  const run = (async () => {
    const uploaded: SubmittedAttachment[] = []
    for (const item of pending) {
      try {
        const res = await forebrainApi.filesUpload(item.file, session, dir)
        uploaded.push({ fileId: res.fileId, filename: item.filename, mediaType: item.mediaType })
      } catch (error) {
        // The dialog stays open so the user can retry; nothing the user
        // attached is lost.
        composerNotice.value = {
          title: t('chat.messageNotSent'),
          detail: `${t('chat.uploadFailed', { name: item.filename })}\n${getErrorMessage(error)}`,
        }
        return
      }
    }
    const target = attachTarget.value
    if (target) subagentComposer(target)?.addUploaded(uploaded)
    else promptRef.value?.addUploaded(uploaded)
  })()
  pendingAttachUpload = run
  try {
    await run
  } finally {
    if (pendingAttachUpload === run) pendingAttachUpload = null
  }
  closeAttach()
}

/** A send that lands while an attach upload is in flight waits for it: the
 *  attachment must be in the composer before the message takes what it holds.
 *  A failed upload resolves too — the message then carries no attachment, and
 *  the failure is already shown. */
async function waitForPendingAttachUpload() {
  while (pendingAttachUpload) {
    await pendingAttachUpload
  }
}

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

/** The caret goes where the reader's next words belong: an ask card's
 *  「和它聊聊」drops the question and hands the turn back to the composer. */
function focusComposer() {
  void nextTick(() => promptRef.value?.focus())
}

/** The conversation's own queue: taking the newest queued message back into
 *  the composer. A subagent's queue is its tab's, handled in that tab. */
async function restoreLastQueuedMessage() {
  const submission = await editLastQueuedMessage()
  if (!submission) return
  promptRef.value?.restoreSubmission(submission)
}

async function interruptAndSendPendingSteers() {
  sendPendingSteersAfterInterrupt()
}

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
  mcpStatus,
  autoContinueForView,
  cancelAutoContinue,
  lspRecommendation,
  pendingActionsVersion,
  pendingInputPreview,
  subagentPendingInput,
  subagentReturnedDraft,
  runtimeStatus,
  takeSubagentReturnedDraft,
  sendToSubagent,
  recallSubagentInput,
  interruptSubagentToSend,
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
  resetForNewChat,
  rebindSessionObserver,
} = useChatStream()

/**
 * The file view this page's composer and workbench address: the open
 * conversation, or — before it exists — the project the composer's pill has
 * chosen. It is the scope the tree, the @ picker and a picked image all
 * resolve their root from.
 */
const workbenchScope = computed<WorkspaceScope>(() => {
  const sid = String(sessionId.value ?? '').trim()
  if (sid) return { sessionId: sid }
  const pid = String(target.value?.projectId ?? '').trim()
  if (pid) return { projectId: pid }
  return {}
})
const { refresh: workspaceTreeRefresh } = useWorkspaceTree(workbenchScope)

/**
 * How much of the run this conversation draws. It is a reading mode, kept per
 * conversation: nothing is deleted, the workbench and the terminal still hold
 * the whole record, and anything waiting for an answer stays in the panel above
 * the composer.
 */
const { viewMode } = useViewMode(() => String(sessionId.value ?? ''))

/**
 * drawnBlocks is what the timeline draws under the mode in force: the same
 * blocks, minus the ones concise withholds. In the default mode it is the list
 * itself, so nothing about a stored message changes.
 */
function drawnBlocks(blocks?: ChatMessage['blocks']): NonNullable<ChatMessage['blocks']> {
  return (blocks ?? []).filter((block) => blockVisibleIn(viewMode.value, block.kind))
}

/**
 * The turn's blocks folded into activity-line runs (owner 2026-10-25): the
 * turn's own action cards hang on one line, everything else keeps its place.
 */
function activityRunsOf(msg: ChatMessage) {
  return groupActivityRuns(drawnBlocks(msg.blocks), blockOnActivityLine)
}

/** Where the turn's first on-line run sits, or -1 when nothing hangs on it. */
function firstLineRunIndexOf(msg: ChatMessage): number {
  return activityRunsOf(msg).findIndex((run) => run.line)
}

/** The turn's todo card, when this mode draws it (it opens the line). */
function planUpdateOf(msg: ChatMessage) {
  if (msg.role !== 'assistant' || viewMode.value !== 'default') return undefined
  return latestPlanUpdate(msg)
}

/**
 * The conversation's own model choice — what runs this session and what it
 * may be switched to. The chip writes it, the top bar names it, and the
 * effort chip reads the levels the catalog offers for the model in force.
 */
const sessionModelData = ref<SessionModelResponse | null>(null)
/** The composer's model chip, so a refusal in the thread can open its menu. */
const modelChipRef = ref<InstanceType<typeof ModelChip> | null>(null)

/**
 * Which of the three image notices a message carries. A refusal recorded before
 * the kinds existed reads as `unsupported`, which is the sentence it was drawn
 * with then.
 */
function imageRefusalKind(msg: { imageRefusal?: { kind?: 'unsupported' | 'rejected' | 'remembered' } }): 'unsupported' | 'rejected' | 'remembered' {
  return msg.imageRefusal?.kind ?? 'unsupported'
}

/**
 * The sentence that goes with that kind: the model's catalog entry says it takes
 * no images, the provider refused one and the turn went on without it, or this
 * conversation already paid for that refusal and the turn was withheld. Three
 * facts, three sentences, one block — and one way out of all three, /model.
 */
function imageRefusalSentence(msg: { imageRefusal?: { kind?: 'unsupported' | 'rejected' | 'remembered' } }): 'chat.imageRefusedLine' | 'chat.imageRejectedNow' | 'chat.imageRejectedRemembered' {
  switch (imageRefusalKind(msg)) {
    case 'rejected':
      return 'chat.imageRejectedNow'
    case 'remembered':
      return 'chat.imageRejectedRemembered'
    default:
      return 'chat.imageRefusedLine'
  }
}

let sessionModelGeneration = 0

/** The composer's approval switch, so a new chat's pick can be carried into its session. */
const approvalPickerRef = ref<InstanceType<typeof ApprovalPresetPicker> | null>(null)

/**
 * What a new chat's model and effort chips have been set to, until its first
 * message opens the conversation and the page writes it as that conversation's
 * own choice. The chips themselves work before there is a session: they read
 * the agent's default (`chatModelDefault`) and the pick is held here.
 */
const draftModel = ref<{ provider: string; model: string; effort: string } | null>(null)

async function loadSessionModel(sid: string | null) {
  const generation = ++sessionModelGeneration
  if (!sid) draftModel.value = null
  try {
    // No session yet: the model a new conversation would start on.
    const res = sid ? await forebrainApi.sessionModel(sid) : await forebrainApi.chatModelDefault()
    if (generation === sessionModelGeneration) sessionModelData.value = res
  } catch {
    // Without the facts the chips stay hidden: nothing is invented for a
    // conversation whose model could not be read.
    if (generation === sessionModelGeneration) sessionModelData.value = null
  }
}

/** A model picked on a new chat: held, with the effort back at the model's default. */
function pickDraftModel(option: { provider: string; model: string }) {
  const data = sessionModelData.value
  if (!data) return
  draftModel.value = { provider: option.provider, model: option.model, effort: '' }
  sessionModelData.value = { ...data, provider: option.provider, model: option.model, effort: '' }
}

watch(sessionId, (sid) => {
  void loadSessionModel(sid)
}, { immediate: true })

/** The reasoning levels the model in force offers, per the shared catalog. */
const sessionEfforts = computed(() => {
  const data = sessionModelData.value
  if (!data) return []
  return data.options.find((option) => option.provider === data.provider && option.model === data.model)?.efforts ?? []
})

/** The effort pick writes the session's own row; the model stays as it is. */
async function selectEffort(effort: string) {
  const sid = sessionId.value
  const data = sessionModelData.value
  if (!data) return
  if (!sid) {
    draftModel.value = { provider: data.provider, model: data.model, effort }
    sessionModelData.value = { ...data, effort }
    return
  }
  try {
    const saved = await forebrainApi.saveSessionModel(sid, { provider: data.provider, model: data.model, effort })
    sessionModelData.value = { ...data, provider: saved.provider, model: saved.model, effort: saved.effort }
  } catch {
    // The chip keeps the state it had; the next load reconciles with the row.
  }
}

/**
 * The directory this conversation runs in, when the session itself says it is
 * gone: the thread carries the read-only notice and the composer takes
 * nothing until the directory is back.
 */
const missingDir = computed(() => {
  const project = sessionProject.value
  return project?.missing ? (project.root ?? '') : null
})

const composerLocked = computed(() => Boolean(missingDir.value))

const nextSubmissionDisposition = ref<'steer' | 'queue'>('steer')

// The right-hand subagent drawer (owner 2026-10-18): which subagents are open
// as tabs, which one is in front, and whether the drawer is shown.
const drawerOpen = ref(false)
const drawerTabs = ref<string[]>([])
const drawerActive = ref('')
/** This conversation's subagents as the "+" menu lists them: a goal's checks
 *  only once opened. */
const conversationSubagents = computed(() => subagents.value.filter((record) => (
  record.agentType !== FOREBRAIN_GOAL_CHECK_AGENT_TYPE || drawerTabs.value.includes(record.agentId)
)))
const drawerRecords = computed(() => drawerTabs.value
  .map((id) => subagents.value.find((record) => record.agentId === id))
  .filter((record): record is SubagentTranscript => Boolean(record)))
// The plan documents open in the same drawer as their own tabs (plan 085): a
// plan's row in the workbench opens its text here, rendered, rather than in a
// dialog over the conversation. planActive is the plan tab in front, empty
// while a subagent's tab is.
const planTabs = ref<PlanTab[]>([])
const planActive = ref('')
// The documents a card head opened, in the same drawer (owner 2026-10-25): a
// whole command or a whole title that its one-line head could not hold. The
// same content is the same tab — docTabFor digests it — so re-opening focuses.
const docTabs = ref<DocTab[]>([])
const docActive = ref('')
// The order the tabs were opened in, across all kinds. The drawer is one
// surface: a plan document opened between two subagents sits between them, the
// way the prototype's single tab array does (:4740). The list only records
// opens — what is actually drawn is derived below, so a stale entry can never
// hide a live tab.
const drawerOrder = ref<string[]>([])
const drawerTabOrder = computed(() => {
  const alive = new Set<string>([
    ...drawerTabs.value,
    ...planTabs.value.map((tab) => tab.id),
    ...docTabs.value.map((tab) => tab.id),
  ])
  const ordered = drawerOrder.value.filter((key) => alive.has(key))
  const known = new Set(ordered)
  for (const id of drawerTabs.value) {
    if (!known.has(id)) {
      known.add(id)
      ordered.push(id)
    }
  }
  for (const tab of planTabs.value) {
    if (!known.has(tab.id)) {
      known.add(tab.id)
      ordered.push(tab.id)
    }
  }
  for (const tab of docTabs.value) {
    if (!known.has(tab.id)) {
      known.add(tab.id)
      ordered.push(tab.id)
    }
  }
  return ordered
})
const drawerVisible = computed(() => drawerOpen.value
  && (drawerRecords.value.length > 0 || planTabs.value.length > 0 || docTabs.value.length > 0)
  && !isFreshChat.value)
/** The tab in front; a closed or unknown id falls back to the first one open. */
const shownActive = computed(() => (
  drawerRecords.value.some((record) => record.agentId === drawerActive.value)
    ? drawerActive.value
    : (drawerRecords.value[0]?.agentId ?? '')
))
const runningSubagentCount = computed(() =>
  conversationSubagents.value.filter((record) => subagentPhase(record.status) === 'active').length)

// Each open tab's composer, so a returned draft, a recalled message or an
// uploaded file reaches the composer of the subagent it belongs to.
type SubagentComposer = {
  takeAttached: () => Pick<ComposerSubmission, 'attachments' | 'mentionImages'>
  hasAttached: () => boolean
  restoreSubmission: (submission: ComposerSubmission, unsent?: File[]) => void
  addUploaded: (attachments: SubmittedAttachment[]) => void
  insertWorkspacePath: (path: string, isDir?: boolean) => void
}
// (A plain record rather than a Map: the icon set this file imports already
// owns the name Map.)
const subagentTabRefs: Record<string, SubagentComposer> = {}
function setSubagentTabRef(agentId: string, el: unknown) {
  if (el) subagentTabRefs[agentId] = el as SubagentComposer
  else delete subagentTabRefs[agentId]
}
function subagentComposer(agentId: string): SubagentComposer | null {
  return subagentTabRefs[String(agentId ?? '').trim()] ?? null
}

/** A refusal or note for one tab, in that tab's own composer. */
const subagentNotices = ref<Record<string, { title: string; detail: string } | null>>({})

// The updatedSeq each subagent was at when the user last looked at it, so the
// drawer can mark the tabs that have said something since.
const seenAgentActivity = ref<Record<string, number>>({})

type ViewBrowseState = {
  scrollTop: number
  follow: boolean
  folds: Record<string, boolean>
}

/** What the drawer was showing the last time this conversation was read. */
type PersistedDrawerState = {
  open: boolean
  tabs: string[]
  active: string
}

type PersistedSessionBrowseState = {
  seenAgentActivity: Record<string, number>
  views: Record<string, ViewBrowseState>
  drawer?: PersistedDrawerState
}

const primaryViewKey = '$primary'
const conversationRef = ref<unknown>(null)
const viewBrowseStates = ref<Record<string, ViewBrowseState>>({})
let restoringBrowseState = false
let browsePersistTimer: ReturnType<typeof setTimeout> | null = null

function browseStorageKey(sid: string): string {
  return `forebrain:chat-browse:v1:${sid}`
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

function emptyDrawerState(): PersistedDrawerState {
  return { open: false, tabs: [], active: '' }
}

/** The drawer a stored entry describes; an entry that never had one opens shut.
 *  An entry written before the drawer existed names an open agent view in a
 *  field of its own, and that is deliberately not migrated: the drawer is a
 *  different surface, so a conversation nobody reopened starts closed. */
function readStoredDrawer(parsed: Partial<PersistedSessionBrowseState>): PersistedDrawerState {
  const drawer = parsed.drawer as Partial<PersistedDrawerState> | undefined
  if (!drawer || typeof drawer !== 'object') return emptyDrawerState()
  return {
    open: drawer.open === true,
    tabs: Array.isArray(drawer.tabs) ? drawer.tabs.filter((id): id is string => typeof id === 'string') : [],
    active: typeof drawer.active === 'string' ? drawer.active : '',
  }
}

function readSessionBrowseState(sid: string): PersistedSessionBrowseState {
  if (typeof window === 'undefined' || !sid) {
    return { seenAgentActivity: {}, views: {}, drawer: emptyDrawerState() }
  }
  try {
    const parsed = JSON.parse(window.localStorage.getItem(browseStorageKey(sid)) ?? '{}') as Partial<PersistedSessionBrowseState>
    return {
      seenAgentActivity: parsed.seenAgentActivity && typeof parsed.seenAgentActivity === 'object'
        ? parsed.seenAgentActivity
        : {},
      views: parsed.views && typeof parsed.views === 'object' ? parsed.views : {},
      drawer: readStoredDrawer(parsed),
    }
  } catch {
    return { seenAgentActivity: {}, views: {}, drawer: emptyDrawerState() }
  }
}

function persistSessionBrowseState(sid = String(sessionId.value ?? '').trim()) {
  if (typeof window === 'undefined' || !sid) return
  try {
    window.localStorage.setItem(browseStorageKey(sid), JSON.stringify({
      seenAgentActivity: seenAgentActivity.value,
      views: viewBrowseStates.value,
      drawer: { open: drawerOpen.value, tabs: drawerTabs.value, active: drawerActive.value },
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

// Only the thread's own scroll is kept — a tab follows its own stream — so the
// state is filed under the one key the conversation has.
function recordCurrentBrowseState() {
  if (restoringBrowseState) return
  const el = conversationElement()
  if (!el) return
  const key = primaryViewKey
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
  const state = browseStateFor(primaryViewKey)
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

// Whether the conversation's queue preview shows anything: recalling the
// newest message is offered exactly as long as one is shown.
const composerQueueVisible = computed(() => {
  const preview = pendingInputPreview.value
  return preview.pendingSteers.length + preview.rejectedSteers.length + preview.queuedMessages.length > 0
})

// The commands a subagent's own view may run, by name and class, from the
// server's own classification (plan 007's D4). Skill commands act on the
// subagent; the rest belong to or are hidden from its view.
const subagentSlashRecords = ref<SlashCommandRecord[]>([])
/** The table is the same for every subagent, so it is fetched once per page. */
let subagentSlashLoaded = false

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

/** A message the boundary gave back to a tab's composer, if any is waiting. */
function applySubagentReturnedDraft(agentId: string) {
  const draft = takeSubagentReturnedDraft(agentId)
  if (draft) subagentComposer(agentId)?.restoreSubmission(draft)
}

/** Takes the newest message waiting in a tab's queue back into its composer. */
async function recallIntoTab(agentId: string) {
  const id = String(agentId ?? '').trim()
  if (!id) return
  const submission = await recallSubagentInput(id)
  if (submission) subagentComposer(id)?.restoreSubmission(submission)
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

/**
 * Opens a subagent as a drawer tab: the tab joins the open ones when it is not
 * there yet and comes to the front either way. The drawer unseats the
 * workbench, because the two share the right-hand column.
 */
async function openSubagentTab(agentId: string) {
  const id = String(agentId ?? '').trim()
  if (!id) return
  if (!drawerTabs.value.includes(id)) drawerTabs.value = [...drawerTabs.value, id]
  if (!drawerOrder.value.includes(id)) drawerOrder.value = [...drawerOrder.value, id]
  drawerActive.value = id
  planActive.value = ''
  drawerOpen.value = true
  workbench.close()
  markAgentSeen(id)
  void loadSubagentBudget(id)
  if (!subagentSlashLoaded) {
    subagentSlashLoaded = true
    void refreshSubagentSlashCommands()
  }
  await nextTick()
  // A draft that came back with a just-ended execution lands in the composer
  // that is now on screen.
  applySubagentReturnedDraft(id)
  scheduleBrowsePersist()
}

/** Brings an already open tab to the front. */
function selectSubagentTab(agentId: string) {
  const id = String(agentId ?? '').trim()
  if (!id) return
  drawerOpen.value = true
  drawerActive.value = id
  planActive.value = ''
  docActive.value = ''
  markAgentSeen(id)
  void loadSubagentBudget(id)
  scheduleBrowsePersist()
}

/**
 * Opens one of the conversation's plan documents as a drawer tab (plan 085):
 * 查看全文 in the workbench's plan row lands here. A plan that is already open
 * is only brought to the front, and the workbench gives up the column the way
 * it does for a subagent.
 */
function openPlanTab(plan: SessionPlan) {
  const id = `plan:${plan.name}`
  if (!planTabs.value.some((tab) => tab.id === id)) {
    const { done, total } = planProgress(plan.markdown)
    planTabs.value = [...planTabs.value, {
      id,
      label: `${t('chat.benchPlan')} · ${planTitle(plan.markdown) || plan.name}`,
      path: plan.path,
      at: formatClock(plan.updatedAt),
      done,
      total,
      markdown: plan.markdown,
    }]
  }
  if (!drawerOrder.value.includes(id)) drawerOrder.value = [...drawerOrder.value, id]
  planActive.value = id
  drawerOpen.value = true
  workbench.close()
  scheduleBrowsePersist()
}

/** Brings an already open plan tab to the front. */
function selectPlanTab(id: string) {
  if (!planTabs.value.some((tab) => tab.id === id)) return
  drawerOpen.value = true
  planActive.value = id
  docActive.value = ''
  scheduleBrowsePersist()
}

/**
 * Closes a plan tab. The document is only being put away — the file stays in
 * the conversation's history — and the tab to its left takes the front. The
 * last tab out of an otherwise empty drawer closes the drawer.
 */
function closePlanTab(id: string) {
  const open = planTabs.value
  const index = open.findIndex((tab) => tab.id === id)
  if (index < 0) return
  const rest = open.filter((tab) => tab.id !== id)
  planTabs.value = rest
  drawerOrder.value = drawerOrder.value.filter((key) => key !== id)
  if (planActive.value === id) planActive.value = rest[rest.length - 1]?.id ?? ''
  if (!rest.length && !drawerRecords.value.length && !docTabs.value.length) drawerOpen.value = false
  scheduleBrowsePersist()
}

/**
 * An output tab's read source, with its retrieval id present — an output tab
 * is only ever opened with one, but the tab type keeps the field optional.
 */
function outputSourceOf(tab: DocTab): { sessionId: string; agentId?: string; retrieveId: number } | undefined {
  const source = tab.source
  if (!source?.sessionId || !source.retrieveId) return undefined
  return source as { sessionId: string; agentId?: string; retrieveId: number }
}

/**
 * Opens one document a card head worded: a whole command, a whole title. The
 * same content is the same tab, so opening it again only brings it to the
 * front. It opens in the conversation's own drawer — from the conversation's
 * cards and from a subagent's alike.
 */
function openDocTab(tab: DocTab) {
  if (!docTabs.value.some((open) => open.id === tab.id)) {
    docTabs.value = [...docTabs.value, tab]
  }
  if (!drawerOrder.value.includes(tab.id)) drawerOrder.value = [...drawerOrder.value, tab.id]
  docActive.value = tab.id
  planActive.value = ''
  drawerOpen.value = true
  scheduleBrowsePersist()
}

/** Brings an already open document tab to the front. */
function selectDocTab(id: string) {
  if (!docTabs.value.some((tab) => tab.id === id)) return
  drawerOpen.value = true
  docActive.value = id
  planActive.value = ''
  scheduleBrowsePersist()
}

/**
 * Closes a document tab; the document is only put away. The tab to its left
 * takes the front, and the last tab out of an otherwise empty drawer closes
 * the drawer.
 */
function closeDocTab(id: string) {
  const open = docTabs.value
  const index = open.findIndex((tab) => tab.id === id)
  if (index < 0) return
  const rest = open.filter((tab) => tab.id !== id)
  docTabs.value = rest
  drawerOrder.value = drawerOrder.value.filter((key) => key !== id)
  if (docActive.value === id) docActive.value = rest[rest.length - 1]?.id ?? ''
  if (!rest.length && !drawerRecords.value.length && !planTabs.value.length) drawerOpen.value = false
  scheduleBrowsePersist()
}

/**
 * Closes a tab. The subagent keeps running — a closed tab is a window shut,
 * not a stop — and the tab to its right takes the front, else the one to its
 * left; the last one out closes the drawer.
 */
function closeSubagentTab(agentId: string) {
  const id = String(agentId ?? '').trim()
  const open = drawerTabs.value
  const index = open.indexOf(id)
  if (index < 0) return
  const rest = open.filter((tab) => tab !== id)
  drawerTabs.value = rest
  drawerOrder.value = drawerOrder.value.filter((key) => key !== id)
  if (drawerActive.value === id) drawerActive.value = rest[index] ?? rest[index - 1] ?? ''
  // The drawer is one surface with three kinds of tab: closing the last
  // subagent while a document is still open puts the document in front
  // instead of hiding what the reader was reading (prototype :4745).
  if (!rest.length) {
    if (!planTabs.value.length && !docTabs.value.length) drawerOpen.value = false
    else if (!planActive.value && !docActive.value) {
      planActive.value = planTabs.value[planTabs.value.length - 1]?.id
        ?? docTabs.value[docTabs.value.length - 1]?.id
        ?? ''
    }
  }
  scheduleBrowsePersist()
}

/** The top bar's control: shut when open, open when shut, opening the first
 *  subagent of the conversation when none is open yet. */
function toggleSubagentDrawer() {
  if (drawerOpen.value) {
    collapseSubagentDrawer()
    return
  }
  if (drawerTabs.value.length || planTabs.value.length || docTabs.value.length) {
    drawerOpen.value = true
    workbench.close()
    scheduleBrowsePersist()
    return
  }
  const first = conversationSubagents.value.find((record) => subagentPhase(record.status) === 'active')
    ?? conversationSubagents.value[0]
  if (first) void openSubagentTab(first.agentId)
}

function collapseSubagentDrawer() {
  drawerOpen.value = false
  scheduleBrowsePersist()
}

// The drawer and the workbench share the right-hand column: opening either
// one puts the other away. The template also refuses to draw both.
watch(() => workbench.open.value, (open) => {
  if (open) drawerOpen.value = false
})

// Staying on a tab keeps it marked read as it works, so a dot means "said
// something while you were elsewhere" and nothing else.
watch(subagents, () => {
  if (drawerVisible.value && shownActive.value) markAgentSeen(shownActive.value)
}, { deep: true })

// A subagent from a previous session is not this session's: the drawer
// reopens on what that conversation had, which is usually nothing.
watch(sessionId, (sid, previousSid) => {
  // A synchronous watch runs before useChatStream clears the old message DOM,
  // so the last scroll anchor is still measurable during A -> B switches.
  if (previousSid) {
    recordCurrentBrowseState()
    persistSessionBrowseState(previousSid)
  }
  const restored = readSessionBrowseState(String(sid ?? '').trim())
  seenAgentActivity.value = restored.seenAgentActivity
  viewBrowseStates.value = restored.views
  const drawer = restored.drawer ?? emptyDrawerState()
  drawerTabs.value = drawer.tabs
  drawerOrder.value = [...drawer.tabs]
  drawerActive.value = drawer.active
  drawerOpen.value = drawer.open
  // A plan document belongs to the conversation whose plans it came from, so
  // the tabs close with it rather than showing another conversation's file.
  planTabs.value = []
  planActive.value = ''
  // One conversation's notices and /context report are not the next one's.
  subagentNotices.value = {}
  subagentContextTarget.value = ''
  subagentContextDebug.value = null
  void restoreCurrentBrowseState()
}, { flush: 'sync' })

/**
 * Esc in a subagent's tab (D1): interrupt it to send what is queued behind it;
 * otherwise cancel its own pending continuation; otherwise collapse the drawer
 * — the web's way of leaving the subagent's view, the same meanings in the same
 * order as the terminal. A message that has already left the composer is the
 * subagent's to take: there is no withdrawing it (plan 142).
 */
async function handleSubagentEscape(agentId: string) {
  const id = String(agentId ?? '').trim()
  if (!id) return
  if (await interruptSubagentToSend(id)) return
  if (autoContinueForView(id)) {
    cancelAutoContinue(id)
    return
  }
  collapseSubagentDrawer()
}

onUnmounted(() => {
  recordCurrentBrowseState()
  persistSessionBrowseState()
  if (browsePersistTimer) clearTimeout(browsePersistTimer)
})

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

// The conversation's one pending-actions store (plan 138), provided down the
// tree: the waiting tool cards read the same loaded requests and answer
// through the same endpoints the thread's tail surface does, so a gate is one
// state wherever it is decided.
const pendingActions = providePendingActions(usePendingActions({
  sessionId: computed(() => sessionId.value),
  version: computed(() => pendingActionsVersion.value),
  contextUsedPercent,
}))

/**
 * The request a tool card is parked on: the timeline's approval block names
 * the call the gate holds, and the store's pending list says whether it is
 * still asking. Null on every settled call.
 */
function gatePendingOf(msg: ChatMessage, stepId: string) {
  const blocks = (msg.blocks ?? []).filter((block): block is Extract<TimelineBlock, { kind: 'approval' }> => block.kind === 'approval')
  return pendingActions.pendingForStep(stepId, blocks)
}

/** The ids whose gates hang on a tool card of this conversation — plus the
 *  plan approval while it waits where the input is — so the tail's own
 *  fallback list holds every request that is not one of them. */
const anchoredActionIds = computed(() => {
  const ids = new Set<string>()
  for (const message of messages.value) {
    for (const block of message.blocks ?? []) {
      if (block.kind !== 'tool') continue
      const found = gatePendingOf(message, block.step.stepId)
      if (found) ids.add(found.id)
    }
  }
  if (exitPlanPending.value) ids.add(exitPlanPending.value.id)
  return ids
})

/** The exit-plan approval taking the input's place, when the conversation's
 *  own planner is the one waiting (a subagent's gate waits in its own view). */
const exitPlanPending = computed(() => {
  const approvals = pendingActions.pendingApprovals.value
  return approvals.find((a) => !a.agentId && pendingActions.isExitPlan(a)) ?? null
})

// The conversation's workspace facts now live behind the workbench's own
// tabs (web 017); this page keeps only its roster and its browse state.
watch(sessionId, () => {
  void loadRoster()
})

const submitStatus = computed(() => (isStreaming.value ? 'streaming' : 'ready'))

// The prototype writes the composer's own words (:3081): what it takes, the
// two characters that reach a command and a file — and, when the conversation's
// directory is gone, the only thing it can say, because it can take nothing.
const composerPlaceholder = computed(() => (
  composerLocked.value ? t('chat.placeholderLocked') : t('chat.placeholder')
))

// One button, two jobs: it submits a turn, or it stops the one running — so its
// name has to say which of the two it is about to do.
const submitLabel = computed(() => (
  isStreaming.value ? t('chat.stopAria') : t('chat.sendAria')
))

/**
 * The run's own line above the composer, in the terminal's order: the state,
 * then the checklist, then the wait for the model's first token. Nothing is
 * drawn while no run is under way. A network retry replaces the whole line:
 * the reason, then the countdown with the attempt.
 */
const runLine = computed(() => {
  const status = runtimeStatus.value
  if (status.kind === 'idle') return null
  if (status.retry) {
    // The once-a-second timer replaces runtimeStatus.value wholesale, so this
    // line re-reads — and re-counts down — every tick; the elapsedMs read
    // keeps that refresh a tracked dependency even if the timer stops
    // replacing the object.
    void status.elapsedMs
    return { ...formatTransportRetryLine(status.retry, t), plan: undefined, tail: '' }
  }
  // formatRuntimeStatusLabel already appends the first-token wait; the
  // terminal puts it last, after the checklist, so it is lifted out here and
  // handed back as the line's trailing segment.
  const { label, plan } = formatRuntimeStatusLabel({ ...status, firstTokenMs: undefined }, t)
  if (!label) return null
  const wait = formatFirstTokenDuration(status.firstTokenMs)
  return { reason: undefined, label, plan, tail: wait ? t('chat.firstToken', { duration: wait }) : '' }
})

/**
 * A roster row's "view": a subagent of this conversation opens in the drawer,
 * one from another conversation opens that conversation and then its tab.
 */
function viewRoster(row: AgentRosterRow) {
  if (row.kind === 'subagent' && subagents.value.some((record) => record.agentId === row.id)) {
    void openSubagentTab(row.id)
    return
  }
  const target = agentRosterConversationTarget(row)
  if (!target) return
  void router.push(target)
}

/** Stops one subagent from its tab; a refusal is that tab's to say. */
async function stopSubagent(agentId: string) {
  const id = String(agentId ?? '').trim()
  if (!id) return
  try {
    await forebrainApi.subagentCancel(id)
  } catch (cause) {
    subagentNotices.value = {
      ...subagentNotices.value,
      [id]: { title: t('chat.requestFailed'), detail: getErrorMessage(cause) },
    }
  }
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

function selectMode(next: string) {
  if (next === 'agent') {
    if (mode.value !== 'agent') exitPlanMode()
    return
  }
  if (next === 'plan') {
    if (mode.value !== 'plan') enterPlanMode()
    return
  }
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

/** The checklist a turn carries now: the last update it made, because the
 *  earlier ones are the same list earlier in the run. */
function latestPlanUpdate(msg: ChatMessage) {
  const updates = msg.planUpdates
  return updates && updates.length ? updates[updates.length - 1] : null
}

function messageBubbleRole(role: string): 'user' | 'assistant' {
  return role === 'user' ? 'user' : 'assistant'
}

/**
 * messageHasBubble reports whether a stored row has anything to say in a bubble.
 *
 * A row that only issued a tool call carries no text at all: its card is what
 * the row means, and an empty bubble beside that card is just noise. The same
 * goes for a row whose only content is a card the timeline already draws, and
 * for a row whose only block is a card-less gate's anchor, which draws
 * nothing — and for a row whose cards the reading mode takes away, which would
 * otherwise leave an empty bubble behind.
 */
function messageHasBubble(msg: ChatMessage): boolean {
  if (String(msg.content ?? '').trim()) return true
  if (msg.attachments?.length) return true
  if (msg.role !== 'assistant') return false
  if (hasVisibleBlocks(msg.blocks) && drawnBlocks(msg.blocks).length) return true
  if (viewMode.value === 'default' && (msg.planUpdates?.length || msg.planBlocks?.length)) return true
  return Boolean(msg.turnDiffs?.length || workedLineOf(msg, t).label)
}

function messageContentClass(role: string): string | undefined {
  if (role === 'user') {
    // The design draws the user's bubble as `.u`, inside a `.u-wrap` that also
    // holds the hover actions: the message column itself carries no chrome.
    // Otherwise the column's own background and padding swallow the actions
    // row (an invisible 24px strip under the words) and the attachment list,
    // and every bubble reads looser than the design.
    return 'gap-0 group-[.is-user]:rounded-none group-[.is-user]:bg-transparent group-[.is-user]:px-0 group-[.is-user]:py-0'
  }
  // The prototype's .a: 13px at 1.55, its parts 10px apart (.a { gap: 10px }).
  return 'w-full min-w-0 max-w-full overflow-visible break-words gap-[10px] text-[13px] leading-[1.55] [&_pre]:max-w-full [&_pre]:overflow-x-auto [&_table]:block [&_table]:max-w-full [&_table]:overflow-x-auto'
}

function isAllowedAttachmentType(mediaType: string, filename: string): boolean {
  const t = (mediaType ?? '').toLowerCase()
  const name = (filename ?? '').toLowerCase()
  return t.startsWith('image/') || t === 'application/pdf' || /\.(png|jpe?g|gif|webp|pdf)$/i.test(name)
}

/**
 * Sends what the composer submitted in a subagent's own view to that subagent.
 * Everything typed there belongs to the subagent: ordinary text runs its next
 * execution, a skill command is sent to it, /context acts on it, a command that
 * acts on the conversation runs there, and the ones hidden there answer with one
 * sentence — plan 007's D4, in this surface. /compact is one of the hidden ones:
 * it compacts the conversation, never a subagent (plan 142).
 */
async function handleSubagentSubmit(agentId: string, message: { text: string; files?: unknown[] }) {
  const id = String(agentId ?? '').trim()
  if (!id) return
  const text = (message.text ?? '').trim()
  const files = message.files ?? []
  const composer = subagentComposer(id)
  const attachedByComposer = composer?.hasAttached() ?? false
  if (!text && files.length === 0 && !attachedByComposer) return
  const refuse = (detail: string) => {
    subagentNotices.value = { ...subagentNotices.value, [id]: { title: t('chat.messageNotSent'), detail } }
  }
  // A shell line is the conversation's, never the subagent's.
  if (text.startsWith('!')) {
    refuse(t('chat.subagentViewCommand'))
    return
  }
  if (text.startsWith('/')) {
    const name = slashCommandName(text)
    if (name === 'context') {
      await openSubagentContext(id)
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
      refuse(t('chat.subagentViewCommand'))
      return
    }
    // A skill command: sent raw; the gateway expands it for this subagent.
  }
  await waitForPendingAttachUpload()
  const attached = composer?.takeAttached() ?? { attachments: [], mentionImages: [] }
  subagentNotices.value = { ...subagentNotices.value, [id]: null }
  // Attachments were saved into the project when they were added (the "save to"
  // dialog), so the submission just carries what the composer already holds.
  const submission: ComposerSubmission = { text, attachments: attached.attachments, mentionImages: attached.mentionImages }
  await sendToSubagent(id, submission)
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
  // The composer is the conversation's own: a subagent's messages are typed in
  // its tab, never here (rule 9).
  const text = (message.text ?? '').trim()
  const files = message.files ?? []
  const attachedByComposer = promptRef.value?.hasAttached() ?? false
  if (!text && files.length === 0 && !attachedByComposer) return
  // Sending a new message fixes a pending rewind on the server; the undo line
  // goes with it, so it never offers to undo something that cannot come back.
  rewindLine.value = null
  // A send that goes through retires the busy notice: whatever ran on the
  // other end has ended, and this turn is the conversation's again.
  busyNotice.value = ''
  if (text === '/memories' && files.length === 0 && !attachedByComposer && !isStreaming.value) {
    await router.push({ name: 'memories' })
    return
  }
  // Everything the composer attached leaves it with this message at once, so
  // an image picked while the files upload belongs to the next one.
  await waitForPendingAttachUpload()
  const attached = promptRef.value?.takeAttached() ?? { attachments: [], mentionImages: [] }
  composerNotice.value = null
  // The first message of a new chat opens its conversation first, in the
  // project the user picked. Without a project — or if the trust prompt is
  // declined — nothing is sent and the message stays in the composer.
  if (!sessionId.value) {
    const opened = await ensureSessionForSend()
    if (!opened) {
      promptRef.value?.restoreSubmission({ text: message.text ?? '', ...attached })
      return
    }
  }
  const activeInputDisposition = isStreaming.value ? nextSubmissionDisposition.value : undefined
  nextSubmissionDisposition.value = 'steer'
  // Files were saved into the project when they were attached (the "save to"
  // dialog), so this send only carries what the composer already holds: its
  // uploaded attachments and its @ images.
  const submission: ComposerSubmission = { text, attachments: attached.attachments, mentionImages: attached.mentionImages }
  await send(text, {
    // The address bar is the one source of which session is open. The
    // composer's submit can land before the route watcher has applied a
    // just-pushed session switch, so read the target here rather than
    // trusting the (possibly one-tick-stale) sessionId ref.
    sessionId: String(route.query.session ?? '').trim() || sessionId.value || undefined,
    attached: submission,
    activeInputDisposition,
  })
}

// A message the boundary handed back to a subagent's composer goes straight
// back into that tab's composer when the tab is mounted; a tab that is not
// open keeps it under its id until it is (openSubagentTab restores it).
watch(subagentReturnedDraft, () => {
  for (const agentId of drawerTabs.value) applySubagentReturnedDraft(agentId)
}, { deep: true })

// A message that never became a turn — withdrawn before it began, handed back
// by a run that ended before taking it, or not taken by a run that had just
// ended — is the user's draft again, with everything it attached.
watch(returnedDraft, () => {
  const draft = takeReturnedDraft()
  if (draft) promptRef.value?.restoreSubmission(draft)
  const notice = takeReturnedNotice()
  const noticeCode = takeReturnedNoticeCode()
  // A busy refusal is the thread's to say: the conversation is running
  // somewhere else, and the block under its last message names why nothing
  // went while the words wait in the composer. Every other refusal stays the
  // composer's own notice.
  if (noticeCode === 'session_running' || noticeCode === 'session_running_elsewhere') {
    busyNotice.value = noticeCode
    return
  }
  // A refusal may arrive as the stable code alone (a subagent that ended before
  // it took the message, plan 143): its sentence is written where it is drawn.
  if (notice || noticeCode) {
    composerNotice.value = { title: t('chat.messageNotSent'), detail: notice ?? '', ...(noticeCode ? { detailCode: noticeCode } : {}) }
  }
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
    void loadRoster()
  }
})

watch(refreshToken, () => {
  void loadRoster()
})

onMounted(async () => {
  workbenchMq = window.matchMedia('(max-width: 1023px)')
  syncWorkbenchOverlay()
  workbenchMq.addEventListener('change', syncWorkbenchOverlay)
  void fetchSessions()
  void loadRoster()
  void loadProjects()
  void (async () => {
    target.value = await defaultTarget()
  })()
})

// The address bar is the one source of which session is open: every entry
// point navigates, and this watch applies the result.
watch(() => route.query.session, (value) => {
  const sid = String(value ?? '').trim()
  // A conversation switch clears the "created project" line and any busy
  // notice: both belong to the thread that was just opened.
  if (sid !== String(sessionId.value ?? '').trim()) {
    createdLine.value = null
    busyNotice.value = ''
  }
  // A new conversation is the page itself: the workbench column belongs to a
  // conversation, so asking for one closes it rather than leaving it behind —
  // and so does the thread, which would otherwise stay on screen under an
  // address that names no session (and keep taking the composer's messages).
  if (!sid) {
    workbench.close()
    if (sessionId.value) resetForNewChat()
  }
  if (sid && sid !== sessionId.value) switchToSession(sid)
}, { immediate: true })

watch(sessionId, (sid) => {
  void loadSessionInfo(sid)
}, { immediate: true })

/**
 * The project whose MCP and language-server entries this conversation would
 * load: the open conversation's own project, or — before one is open — the
 * project the composer has picked. A conversation that belongs to no project
 * has no project scope, so there is nothing to ask about.
 */
const projectServersScopeId = computed(() => {
  const sid = String(sessionId.value ?? '').trim()
  if (sid) return String(sessionProject.value?.id ?? '').trim()
  return String(target.value?.projectId ?? '').trim()
})

/** The entries the project declares and nobody has confirmed yet, in the two
 * groups the dialog asks about. */
const projectServersMcp = ref<ProjectServerPendingRow[]>([])
const projectServersLsp = ref<ProjectServerPendingRow[]>([])
const projectServersOpen = ref(false)
const projectServersBusy = ref(false)
const projectServersError = ref('')
let projectServersGeneration = 0

/**
 * Read what the dialog would ask about: the project's undecided MCP and
 * language-server entries in one read. Nothing pending keeps the dialog
 * closed; a read that could not be made is not an empty list — the entries
 * stay pending and the next session asks again.
 */
async function loadProjectServers(projectId: string): Promise<void> {
  const generation = ++projectServersGeneration
  try {
    const answer = await forebrainApi.projectServersPending(projectId)
    if (generation !== projectServersGeneration) return
    projectServersMcp.value = answer.mcp
    projectServersLsp.value = answer.lsp
    projectServersOpen.value = answer.mcp.length > 0 || answer.lsp.length > 0
  } catch (cause) {
    if (generation !== projectServersGeneration) return
    projectServersError.value = getErrorMessage(cause)
  }
}

/**
 * Record the whole answer at once: every entry the dialog listed is decided —
 * checked means allowed, unchecked means declined — and the switch turns on
 * with it. Each choice carries the fingerprint of the row the dialog showed,
 * so a project file that moved under the operator is refused whole and asked
 * about again.
 */
async function answerProjectServers(payload: { allowed: Record<string, boolean>; alwaysAllow?: boolean }): Promise<void> {
  const projectId = projectServersScopeId.value
  if (!projectId || projectServersBusy.value) return
  projectServersBusy.value = true
  projectServersError.value = ''
  const choice = (rows: ProjectServerPendingRow[]) =>
    rows.map((row) => ({ name: row.name, fingerprint: row.fingerprint, allow: payload.allowed[row.name] === true }))
  try {
    await forebrainApi.projectServersConsent(projectId, {
      mcp: choice(projectServersMcp.value),
      lsp: choice(projectServersLsp.value),
      alwaysAllow: payload.alwaysAllow === true,
    })
    // The read decides whether anything is still asking: nothing pending
    // closes the dialog, and entries that changed during the flight come back
    // as rows of their own.
    await loadProjectServers(projectId)
    // The answer is what this session's own MCP reconcile will find, so the
    // conversation is bound again: the gateway reconciles on every bind, and
    // the entries the reader allowed are up in the session they are in.
    rebindSessionObserver()
  } catch (cause) {
    projectServersError.value = getErrorMessage(cause)
    // The list is read again, so an entry that changed under the dialog is
    // shown again with its new fingerprint instead of the one that failed,
    // and the dialog stays open to ask about what is still pending.
    await loadProjectServers(projectId)
  } finally {
    projectServersBusy.value = false
  }
}

function confirmProjectServers(payload: { allowed: Record<string, boolean>; alwaysAllow: boolean }): void {
  void answerProjectServers(payload)
}

/** Decline every listed entry — the button, and the dialog's own close (×,
 * Escape, the mask): both are recorded, so nothing asked is asked again. */
function declineAllProjectServers(): void {
  void answerProjectServers({ allowed: {} })
}

// A conversation that opens re-resolves what it would load, so the entries
// awaiting an answer are read when the conversation changes and when the
// project it belongs to changes.
watch([sessionId, projectServersScopeId], () => {
  const projectId = projectServersScopeId.value
  if (!projectId) {
    projectServersMcp.value = []
    projectServersLsp.value = []
    projectServersOpen.value = false
    return
  }
  void loadProjectServers(projectId)
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
    // A real session is open, so the new-conversation marker has done its job.
    const query: Record<string, string> = {}
    for (const [key, value] of Object.entries(route.query)) {
      if (key !== 'new' && typeof value === 'string') query[key] = value
    }
    query.session = sid
    void router.replace({ query })
  }
})

/**
 * `?agent=<id>` asks for one subagent's tab of the open conversation — the way
 * a roster jump from another page lands on it. The parameter is spent as soon
 * as it is answered, so a reload does not reopen what the user closed.
 */
watch([() => route.query.agent, () => subagents.value.length, historyLoading], () => {
  const agentId = String(route.query.agent ?? '').trim()
  if (!agentId) return
  if (subagents.value.some((record) => record.agentId === agentId)) {
    void openSubagentTab(agentId)
  } else if (historyLoading.value) {
    // Still loading: the subagent may yet arrive with its event pages.
    return
  }
  // Nothing answers to that id: the parameter is spent either way.
  const query: Record<string, string> = {}
  for (const [key, value] of Object.entries(route.query)) {
    if (key !== 'agent' && typeof value === 'string') query[key] = value
  }
  void router.replace({ query })
}, { immediate: true })



onUnmounted(() => {
  workbenchMq?.removeEventListener('change', syncWorkbenchOverlay)
})
</script>

<style scoped>
/* The prototype's .col (chat column) and .thread padding: the scroll container
   spans the pane so the scrollbar sits at its right edge, while the column —
   width and breathers — is what the reader reads. */
.chat-thread-col {
  width: min(760px, 100% - 48px);
  margin-inline: auto;
}

/* The prototype's .a: the parts of one answer sit 10px apart, and a card's own
   margins never add to that. */
.msg-blocks {
  display: grid;
  gap: 10px;
}

/* The prototype's .al-say: the assistant's own words hang on the brand mark,
   a size up from the cards (14px / 1.75), aligned with the line's content
   column. */
.msg-blocks :deep(.al-say) {
  font-size: 14px;
  line-height: 1.75;
  color: var(--forebrain-text);
}

/* New-conversation page (plan 029): the greeting above the composer, the
   project pill at the composer's top-left, and the quick starts below it. */
.chat-greeting {
  font-size: 22px;
  font-weight: 600;
  letter-spacing: -0.01em;
  color: var(--forebrain-text);
}

/* The new-conversation page is one centred column (prototype .nc, owner
   2026-10-18): greeting, composer and quick starts centred as a group, 22px
   apart, inside a 24px inset — so the pane's top and bottom insets match
   whatever the group's height is. */
.chat-col--fresh {
  justify-content: center;
  align-items: center;
  gap: 22px;
  padding: 24px;
}

.chat-col--fresh > * {
  flex: none;
}

/* The prototype's .composer: the same width as the column above it, with the
   pane's bottom breather carried by its own margin. */
.chat-composer {
  flex: none;
  width: min(760px, 100% - 48px);
  margin: 0 auto 16px;
}

.chat-col--fresh .chat-composer {
  margin-bottom: 0;
}

.chat-col--fresh .starters {
  padding-top: 22px;
}

/* The prototype's .composer .ctop (:875): the project pill inside the box's
   top edge, 8px down and 10px in from it. */
.ctop,
.composer-ctop {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 8px 10px 0;
  position: relative;
}

/* A conversation whose directory is gone can take nothing: the box says so by
   its surface alone (prototype .composer.locked). */
.composer-locked :deep([data-slot='input-group']) {
  background: var(--forebrain-bg-alt);
}

.starters {
  display: flex;
  flex-wrap: wrap;
  gap: 10px;
  justify-content: center;
  padding-top: 12px;
}

.starter {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  height: 32px;
  padding: 0 12px;
  border: 1px solid var(--forebrain-divider);
  border-radius: 8px;
  background: var(--forebrain-surface);
  color: var(--forebrain-text-2);
  font-size: 12.5px;
  cursor: pointer;
}

.starter:hover {
  border-color: var(--forebrain-divider-strong);
  color: var(--forebrain-text);
}

/* The prototype's .sys-line: a muted 12px line centred in the column, with the
   green kept for its icon alone (prototype :776-777). */
.sys-line {
  justify-self: center;
  display: flex;
  align-items: center;
  gap: 6px;
  width: 100%;
  justify-content: center;
  font-size: 12px;
  color: var(--forebrain-muted-text);
  padding: 4px 0;
}

.sys-line svg.lucide {
  width: 14px;
  height: 14px;
  color: var(--forebrain-success);
}

/* The prototype's .u-wrap / .u / .u-acts (plan 033): the user's own words are
   the bubble and nothing else — the actions sit under it on the page, and what
   the message attached sits in it. */
.u-wrap {
  display: grid;
  justify-items: end;
  gap: 2px;
}
.u {
  justify-self: end;
  /* The prototype's .u: no width of its own — the bubble is sized to the words
     and capped at 80% of the column — so a long message stays inside the row. */
  max-width: 80%;
  background: var(--forebrain-brand-soft);
  border-radius: 10px;
  padding: 9px 13px;
}
.u-origin {
  justify-self: end;
  font-size: 11px;
  color: var(--forebrain-muted-text);
}
.u-acts {
  display: flex;
  gap: 2px;
  opacity: 0;
  transition: opacity 120ms ease;
}
.u-wrap:hover .u-acts,
.u-wrap:focus-within .u-acts {
  opacity: 1;
}
.u-acts .icon-btn {
  display: inline-flex;
  align-items: center;
  justify-content: center;
  width: 28px;
  height: 28px;
  border-radius: 6px;
  color: var(--forebrain-muted-text);
}
.u-acts .icon-btn:hover:not(:disabled) {
  background: var(--forebrain-button-alt-hover-bg);
  color: var(--forebrain-text-2);
}
.u-acts .icon-btn:disabled {
  opacity: 0.4;
  cursor: not-allowed;
}
@media (prefers-reduced-motion: reduce) {
  .u-acts {
    transition: none;
  }
}
.rewind-undo {
  font-size: 12px;
  color: var(--forebrain-brand-1);
  background: none;
  border: none;
  padding: 0;
  cursor: pointer;
}
.rewind-undo:hover {
  text-decoration: underline;
}

</style>
