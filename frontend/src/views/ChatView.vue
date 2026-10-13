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
        <div class="min-h-0 flex-1 overflow-hidden">
          <!-- The new-conversation page: a greeting above the composer, the
               project pill and quick starts around it. It sits bottom-aligned
               so nothing the pill's chooser opens can be clipped. -->
          <div
            v-if="isFreshChat"
            class="flex h-full flex-col items-center justify-end px-6 pb-2"
            data-testid="chat-greeting"
          >
            <h2 class="chat-greeting">{{ t('chat.greeting') }}</h2>
          </div>
          <Conversation
            v-else
            ref="conversationRef"
            class="chat-scrollbar relative h-full overflow-y-scroll overscroll-contain"
            @scroll.passive="recordCurrentBrowseState"
          >
            <ConversationContent>
            <!-- A new conversation whose first message created its project
                 says so once, at the top of the thread. -->
            <div v-if="createdLine" class="sys-line" data-testid="created-project-line">
              <FolderPlus class="size-4" aria-hidden="true" />
              {{ t('chat.createdProject', { name: createdLine.name }) }}
            </div>
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
                        class="inline-flex items-center rounded-lg bg-muted px-4 py-3">
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
                      <template v-else-if="hasVisibleBlocks(msg.blocks)">
                        <template v-for="(block, blockIdx) in msg.blocks" :key="`${msg.id ?? idx}-block-${blockIdx}`">
                          <!-- A thinking block reads as the prototype's .think:
                               small, muted, no border of its own. The label
                               above the text names the block and how long the
                               model thought — the same label the subagent view
                               shows, so a block reads the same in both. -->
                          <div v-if="block.kind === 'thinking'" class="mb-2">
                            <div class="text-[11px] text-[var(--forebrain-muted-text)]">
                              {{ thinkingBlockLabel(block, t) }}
                            </div>
                            <div class="think-block whitespace-pre-wrap break-words">
                              {{ reasoningDisplayText(block.text) }}
                            </div>
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
                      <div v-else-if="msg.role === 'user' && String(msg.content ?? '').trim()" class="u-wrap">
                        <div class="u">
                          <p v-if="originLabel(msg.origin)" data-testid="message-origin"
                            class="mb-1 text-[11px] font-medium text-[var(--forebrain-muted-text)]">{{ originLabel(msg.origin) }}</p>
                          <div class="whitespace-pre-wrap break-words">{{ msg.content }}</div>
                        </div>
                        <!-- The prototype's .u-acts: on hover (or keyboard focus) the
                             two actions the message offers — rewind to here and copy. -->
                        <div class="u-acts">
                          <button
                            type="button"
                            class="icon-btn"
                            :title="isStreaming ? t('chat.rewindBusy') : t('chat.rewind')"
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
                            :title="t('chat.copyMessage')"
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
            <!-- A run parked on an approval or a question is answered here, in
                 the thread it stopped: the card carries the plan, the
                 questions and the permission decisions, anchored after the
                 last turn the way the terminal's approval card sits in the
                 transcript. -->
            <ApprovalCard
              :session-id="sessionId"
              :version="pendingActionsVersion"
              :context-used-percent="contextUsedPercent"
              can-open-agent
              @open-agent="openAgentView"
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
              </div>
            </ConversationContent>
            <ConversationScrollButton />
          </Conversation>
        </div>


        <div class="z-10 shrink-0 border-t border-[var(--forebrain-divider)] bg-[var(--forebrain-surface)] px-3 py-4 sm:px-5">
          <!-- The new-conversation composer: the project pill sits at its
               top-left, naming (and choosing) where the conversation runs. -->
          <div v-if="isFreshChat" class="ctop">
            <ProjectPill
              ref="pillRef"
              :projects="projects"
              :selected="target"
              :invalid="pillInvalid"
              @select="pickTarget"
              @browse="openFolderDialog"
            />
          </div>
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
                    :disabled="composerLocked" :scope="workbenchScope" attach-save
                    @files-picked="onFilesPicked"
                    @queue-follow-up="markNextSubmissionAsQueued"
                    @edit-last-queued="restoreLastQueuedMessage"
                    @interrupt-run="interruptAndSendPendingSteers"
                    @typed="cancelAutoContinue(activeAgentView)"
                    @escape="cancelAutoContinue(activeAgentView)" />
                </PromptInputBody>
                <PromptInputFooter>
                  <!-- The composer's own bar, in the prototype's order: on the
                       left the "+", the mode and the approval preset; on the
                       right the context ring, the model, the reasoning effort,
                       then send. -->
                  <div class="flex min-w-0 flex-1 items-center gap-1">
                    <ComposerPlusMenu :disabled="composerLocked" @insert="insertPlusCommand" />
                    <ModeChip :mode="mode" :disabled="isStreaming || composerLocked" @select="selectMode" />
                    <ApprovalPresetPicker :session-id="sessionId" />
                  </div>
                  <div class="flex items-center gap-1">
                    <ContextRing
                      v-if="contextUsedPercent != null"
                      :percent="contextUsedPercent"
                      @open="workbench.toggle()"
                    />
                    <ModelChip
                      v-if="sessionModelData"
                      :session-id="sessionId ?? ''"
                      :selection="sessionModelData"
                      :options="sessionModelData.options"
                      :disabled="composerLocked"
                      @saved="loadSessionModel(sessionId ?? '')"
                    />
                    <EffortChip
                      v-if="sessionModelData && sessionEfforts.length"
                      :effort="sessionModelData.effort"
                      :efforts="sessionEfforts"
                      :disabled="composerLocked"
                      @select="selectEffort"
                    />
                    <PromptInputSubmit :status="submitStatus" :disabled="composerLocked" />
                  </div>
                </PromptInputFooter>
              </PromptInput>
            </div>
          </PromptInputProvider>
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
      <ChatWorkbench
        v-if="workbench.open.value"
        :records="runningRoster"
        :loading="rosterLoading"
        :error="rosterError"
        :overlay="workbenchOverlay"
        :scope="workbenchScope"
        :root-path="sessionProject?.root ?? ''"
        @close="workbench.close()"
        @view="viewRoster"
        @cancel="cancelRoster"
        @cancel-all="cancelAll"
        @insert-ref="insertWorkspaceRef"
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
import { h, ref, computed, nextTick, onMounted, onUnmounted, watch } from 'vue'
import { RouterLink, useRoute, useRouter } from 'vue-router'
import { MessageSquare, CheckCircle, CircleAlert, Bot, User, Image as ImageIcon, Paperclip, PanelRight, FolderPlus, FileText, Map, Hammer, Clock, History, Copy } from 'lucide-vue-next'
import { Alert, AlertDescription, AlertTitle } from '@repo/shadcn-vue/components/ui/alert'
import { Avatar, AvatarFallback } from '@repo/shadcn-vue/components/ui/avatar'
import AgentViewTabs from '@/components/chat/AgentViewTabs.vue'
import ProjectPill from '@/components/chat/ProjectPill.vue'
import FolderDialog from '@/components/chat/FolderDialog.vue'
import RewindDialog from '@/components/chat/RewindDialog.vue'
import TrustDialog from '@/components/chat/TrustDialog.vue'
import SessionHeartbeat from '@/components/chat/SessionHeartbeat.vue'
import SubagentCallCard from '@/components/chat/SubagentCallCard.vue'
import SubagentConversation from '@/components/chat/SubagentConversation.vue'
import SlashPickerCard from '@/components/chat/SlashPickerCard.vue'
import PlanUpdateCard from '@/components/chat/PlanUpdateCard.vue'
import ToolCallCard from '@/components/chat/ToolCallCard.vue'
import ApprovalCard from '@/components/chat/ApprovalCard.vue'
import McpNotice from '@/components/chat/McpNotice.vue'
import ComposerPlusMenu from '@/components/chat/ComposerPlusMenu.vue'
import ModeChip from '@/components/chat/ModeChip.vue'
import ModelChip from '@/components/chat/ModelChip.vue'
import EffortChip from '@/components/chat/EffortChip.vue'
import ContextRing from '@/components/chat/ContextRing.vue'
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
  PromptInputBody,
  PromptInputFooter,
  PromptInputProvider,
  PromptInputSubmit,
} from '@repo/elements/prompt-input'
import ForebrainPromptTextarea, { type BotOption } from '@/components/ForebrainPromptTextarea.vue'
import ChatWorkbench from '@/components/chat/ChatWorkbench.vue'
import ApprovalPresetPicker from '@/components/chat/ApprovalPresetPicker.vue'
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
import { useChatStream, workedLineOf, emptyPendingInputPreview, hasVisibleBlocks, thinkingBlockLabel, type ChatMessage, type PlanBlock } from '@/composables/useChatStream'
import { useLastSession } from '@/composables/useLastSession'
import { useTopbar } from '@/composables/useTopbar'
import { useWorkbench } from '@/composables/useWorkbench'
import { useWorkspaceTree, migrateWorkspaceTree } from '@/composables/useWorkspaceTree'
import { useChatSessions } from '@/composables/useChatSessions'
import { useSessionInfo, sessionHeaderTitle } from '@/composables/useSessionInfo'
import { usePrimaryAgents } from '@/composables/usePrimaryAgents'
import { getErrorMessage, forebrainApi, type AgentRosterRow, type ChatAttachmentRecord, type ProjectRecord, type RewindPreview, type SessionContextDebug, type SessionModelResponse, type SlashCommandRecord, type WorkspaceScope } from '@/lib/api'
import { targetFromFolder, targetFromProject, type ProjectTarget } from '@/lib/projectTarget'
import { formatProviderError } from '@/lib/providerError'
import type { ComposerSubmission, PickedFile, SubmittedAttachment } from '@/lib/composerSubmission'
import AttachSaveDialog, { type AttachFileRow } from '@/components/chat/AttachSaveDialog.vue'
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

/** The conversation's name, read from the session itself. */
const sessionTitle = computed(() => sessionHeaderTitle(sessionInfo.value, t('nav.chat')))
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
const busyNotice = ref(false)
const busyNoticeText = computed(() => busyNotice.value
  ? (formatProviderError({ code: 'session_running' }, locale.value) ?? '')
  : '')

// The factories run inside the shell's render, so the row tracks this
// conversation's own state — title, project, model, heartbeat, roster —
// without the page re-registering anything.
setTopbar({
  leading: () => [
    h('span', {
      class: 'truncate text-[13px] font-semibold text-[var(--forebrain-text)]',
      'data-testid': 'chat-session-title',
    }, sessionTitle.value),
    // A project's conversation says whose it is and leads back to that
    // project's space, where its sessions are listed.
    ...(sessionProject.value ? [(() => {
      const project = sessionProject.value!
      return h(RouterLink, {
        to: `/projects/${encodeURIComponent(project.id)}/overview`,
        class: 'chat-project-tag',
        'data-testid': 'chat-project-tag',
      }, { default: () => t('chat.projectTag', { name: project.name }) })
    })()] : []),
    // The model this conversation runs on, in the secondary colour.
    ...(sessionModelData.value ? [h('span', { class: 'forebrain-topbar-model' }, sessionModelData.value.model)] : []),
  ],
  trailing: () => [
    // The heartbeat is this conversation's, so its control sits in the
    // conversation's row; without a conversation there is nothing for it to
    // ask again.
    ...(sessionId.value ? [h(SessionHeartbeat, { sessionId: sessionId.value })] : []),
    h('button', {
      type: 'button',
      class: 'forebrain-chip-btn',
      'aria-pressed': String(workbench.open.value),
      'data-testid': 'workbench-toggle',
      onClick: () => workbench.toggle(),
    }, [
      h(PanelRight, { class: 'size-3.5', 'aria-hidden': 'true' }),
      h('span', {}, t('chat.workbench')),
      // Only agents actually running reach the toggle's badge.
      ...(runningRoster.value.length ? [h('span', { class: 'forebrain-chip-count' }, String(runningRoster.value.length))] : []),
    ]),
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
  !sessionId.value && !messages.value.length && !isStreaming.value && !activeAgentView.value)

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
 */
async function onFilesPicked(files: PickedFile[]) {
  const allowed = files.filter((f) => isAllowedAttachmentType(f.mediaType, f.filename))
  if (!allowed.length) return
  const opened = await ensureSessionForSend()
  if (!opened) return
  const project = await attachProjectFor(opened)
  if (!project) return
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
    promptRef.value?.addUploaded(uploaded)
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
  mcpStatus,
  autoContinueForView,
  cancelAutoContinue,
  lspRecommendation,
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
 * The conversation's own model choice — what runs this session and what it
 * may be switched to. The chip writes it, the top bar names it, and the
 * effort chip reads the levels the catalog offers for the model in force.
 */
const sessionModelData = ref<SessionModelResponse | null>(null)
let sessionModelGeneration = 0

async function loadSessionModel(sid: string | null) {
  const generation = ++sessionModelGeneration
  if (!sid) {
    sessionModelData.value = null
    return
  }
  try {
    const res = await forebrainApi.sessionModel(sid)
    if (generation === sessionModelGeneration) sessionModelData.value = res
  } catch {
    // Without the facts the chips stay hidden: nothing is invented for a
    // conversation whose model could not be read.
    if (generation === sessionModelGeneration) sessionModelData.value = null
  }
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
  if (!sid || !data) return
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

// The conversation's workspace facts now live behind the workbench's own
// tabs (web 017); this page keeps only its roster and its browse state.
watch(sessionId, () => {
  void loadRoster()
})

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
 * nothing.
 */
function messageHasBubble(msg: ChatMessage): boolean {
  if (String(msg.content ?? '').trim()) return true
  if (msg.attachments?.length) return true
  if (msg.role !== 'assistant') return false
  return Boolean(
    hasVisibleBlocks(msg.blocks) ||
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

/** The thinking block's body, as the prototype's .think draws it: plain,
 *  small, muted — the duration label arrives with the transcript change that
 *  records it (plans/016), not invented here. */
function reasoningDisplayText(content: string): string {
  return String(content ?? '').trim()
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
  await waitForPendingAttachUpload()
  const attached = promptRef.value?.takeAttached() ?? { attachments: [], mentionImages: [] }
  composerNotice.value = null
  const queued = nextSubmissionDisposition.value === 'queue'
  nextSubmissionDisposition.value = 'steer'
  // Attachments were saved into the project when they were added (the "save to"
  // dialog), so the submission just carries what the composer already holds.
  const submission: ComposerSubmission = { text, attachments: attached.attachments, mentionImages: attached.mentionImages }
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
  // Sending a new message fixes a pending rewind on the server; the undo line
  // goes with it, so it never offers to undo something that cannot come back.
  rewindLine.value = null
  // A send that goes through retires the busy notice: whatever ran on the
  // other end has ended, and this turn is the conversation's again.
  busyNotice.value = false
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
  // A busy refusal is the thread's to say: the conversation is running
  // somewhere else, and the block under its last message names why nothing
  // went while the words wait in the composer. Every other refusal stays the
  // composer's own notice.
  if (noticeCode === 'session_running') {
    busyNotice.value = true
    return
  }
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
  const qSid = String(route.query.session ?? '').trim()
  // `new=1` asks for the new-conversation page: it must not jump back to the
  // last-used conversation.
  if (!qSid && route.query.new) return
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
  // A conversation switch clears the "created project" line and any busy
  // notice: both belong to the thread that was just opened.
  if (sid !== String(sessionId.value ?? '').trim()) {
    createdLine.value = null
    busyNotice.value = false
  }
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
    // A real session is open, so the new-conversation marker has done its job.
    const query: Record<string, string> = {}
    for (const [key, value] of Object.entries(route.query)) {
      if (key !== 'new' && typeof value === 'string') query[key] = value
    }
    query.session = sid
    void router.replace({ query })
  }
})



onUnmounted(() => {
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

/* A thinking block, as the prototype's .think: small, muted, no border of
   its own — a paragraph of the turn's own reasoning, not a panel. */
.think-block {
  font-size: 12.5px;
  line-height: 1.6;
  color: var(--forebrain-muted-text);
}

/* New-conversation page (plan 029): the greeting above the composer, the
   project pill at the composer's top-left, and the quick starts below it. */
.chat-greeting {
  font-size: 22px;
  font-weight: 600;
  letter-spacing: -0.01em;
  color: var(--forebrain-text);
}

.ctop {
  display: flex;
  align-items: center;
  gap: 10px;
  padding: 0 2px 8px;
  position: relative;
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

.sys-line {
  display: inline-flex;
  align-items: center;
  gap: 6px;
  font-size: 12.5px;
  color: var(--forebrain-success);
  padding: 4px 0;
}

/* The prototype's .u-wrap / .u-acts (plan 033): a user message's actions sit
   under its bubble, right-aligned, and appear on hover or keyboard focus. */
.u-wrap {
  display: grid;
  justify-items: end;
  gap: 2px;
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
  width: 24px;
  height: 24px;
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
