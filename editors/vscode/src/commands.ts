// The two commands that send editor context into a conversation, plus the
// plumbing they share.
//
// Every decision lives elsewhere and is tested there: sessionTarget.ts picks the
// conversation, promptBuilder.ts writes the text, deliverableGate.ts and
// approvalText.ts guard the other two surfaces. This file only reads the
// editor, asks the one question, and calls the gateway.
import * as vscode from 'vscode';
import type { GatewayClient } from './gateway';
import { buildFilePrompt, buildSelectionPrompt, diagnosticsOf } from './promptBuilder';
import {
  newestSession,
  projectForFolder,
  resolveTargetSession,
  sessionTitle,
  type SelectionTarget,
} from './sessionTarget';
import { TurnRefusedError } from './sessionSocket';

const LAST_TARGET = 'forebrain.lastTargetSession';

/** The engine's four "this session is busy on another surface" refusal codes. */
const BUSY_CODES = new Set([
  'session_running',
  'session_awaiting_approval',
  'session_running_elsewhere',
  'session_awaiting_approval_elsewhere',
]);

interface ProjectsAnswer {
  readonly projects?: readonly { readonly id: string; readonly root: string }[];
}

interface SessionsAnswer {
  readonly records?: readonly { readonly id: string }[];
}

export interface CommandDeps {
  /** `forebrain.lastTargetSession`: the conversation the last command sent into. */
  readonly workspaceState: vscode.Memento;
  readonly connected: () => boolean;
  readonly token: () => string;
  readonly client: () => Pick<GatewayClient, 'getJson' | 'postJson'>;
  /** What the relay saw the Web UI read: the conversation the panel is showing. */
  readonly observedSession: () => string | undefined;
  readonly showSession: (id: string) => Promise<void>;
  /** Binds the host socket to the session when needed, then start_run; resolves with the run id. */
  readonly startTurn: (sessionId: string, content: string) => Promise<string>;
  readonly focusPanel: () => void;
  readonly log: (line: string) => void;
}

export class ForebrainCommands {
  constructor(private readonly deps: CommandDeps) {}

  /** "Ask About Selection": the question is required; Esc cancels and creates nothing. */
  async askAboutSelection(): Promise<void> {
    if (!this.guard()) return;
    const editor = vscode.window.activeTextEditor;
    if (!editor) {
      void vscode.window.showWarningMessage(vscode.l10n.t('Forebrain: open a file first.'));
      return;
    }
    const { document, selection } = editor;
    if (selection.isEmpty) {
      void vscode.window.showWarningMessage(vscode.l10n.t('Forebrain: select some code first.'));
      return;
    }
    const question = await this.ask(vscode.l10n.t('Ask Forebrain about the selection'));
    if (question === undefined) return;
    const context = {
      fsPath: document.uri.fsPath,
      relPath: vscode.workspace.asRelativePath(document.uri, false),
      languageId: document.languageId,
      startLine: selection.start.line + 1,
      endLine: selection.end.line + 1,
      selection: document.getText(selection),
    };
    await this.send(document.uri, buildSelectionPrompt(context, question, this.diagnostics(document.uri)));
  }

  /** "Ask About File": the whole file as context. */
  async askAboutFile(uri?: vscode.Uri): Promise<void> {
    if (!this.guard()) return;
    const target = uri instanceof vscode.Uri ? uri : vscode.window.activeTextEditor?.document.uri;
    if (!target) {
      void vscode.window.showWarningMessage(vscode.l10n.t('Forebrain: open a file first.'));
      return;
    }
    if (target.scheme !== 'file') {
      void vscode.window.showWarningMessage(vscode.l10n.t('Forebrain: that is not a file on disk.'));
      return;
    }
    const question = await this.ask(vscode.l10n.t('Ask Forebrain about this file'));
    if (question === undefined) return;
    const bytes = await vscode.workspace.fs.readFile(target);
    const content = Buffer.from(bytes).toString('utf8');
    const document = await vscode.workspace.openTextDocument(target);
    const context = {
      fsPath: target.fsPath,
      relPath: vscode.workspace.asRelativePath(target, false),
      languageId: document.languageId,
      content,
    };
    await this.send(target, buildFilePrompt(context, question, this.diagnostics(target)));
  }

  /** True when the panel is connected; a warning names what is missing otherwise. */
  private guard(): boolean {
    if (this.deps.connected()) return true;
    void vscode.window.showWarningMessage(vscode.l10n.t('Forebrain is not connected. Open the Forebrain panel and connect first.'));
    return false;
  }

  private async ask(what: string): Promise<string | undefined> {
    const answer = await vscode.window.showInputBox({
      prompt: what,
      placeHolder: vscode.l10n.t('e.g. There is a bug here — fix it'),
      ignoreFocusOut: true,
    });
    if (answer === undefined) return undefined; // Esc: nothing is created, the panel does not move
    // An empty answer is a cancel too: a turn with no question has nothing to send.
    return answer.trim() === '' ? undefined : answer;
  }

  private diagnostics(uri: vscode.Uri) {
    if (!vscode.workspace.getConfiguration('forebrain').get<boolean>('includeDiagnostics', true)) return [];
    return diagnosticsOf(vscode.languages.getDiagnostics(uri));
  }

  private async send(uri: vscode.Uri, prompt: string): Promise<void> {
    const folder = vscode.workspace.getWorkspaceFolder(uri);
    if (!folder) {
      void vscode.window.showWarningMessage(vscode.l10n.t('Forebrain: that file is not inside a workspace folder.'));
      return;
    }
    const setting = vscode.workspace.getConfiguration('forebrain').get<SelectionTarget>('selectionTarget', 'current');
    try {
      const observed = this.deps.observedSession();
      const target = resolveTargetSession({
        setting,
        explicit: this.deps.workspaceState.get<string>(LAST_TARGET),
        observed,
        recent: await this.recent(folder.uri.fsPath),
      }) ?? (await this.create(folder.uri.fsPath, uri));
      // Steering the panel to the conversation it is already showing would
      // rebuild the iframe, reload the Web UI and cut a reply that is streaming;
      // a new turn reaches the panel through its own connection anyway.
      if (target !== observed) await this.deps.showSession(target);
      await this.deps.startTurn(target, prompt);
      await this.deps.workspaceState.update(LAST_TARGET, target);
      this.deps.focusPanel();
    } catch (err) {
      if (err instanceof TurnRefusedError && BUSY_CODES.has(err.code)) {
        // Expected: another surface is running this conversation. The engine's
        // own sentence says what to do, and there is nothing to retry.
        void vscode.window.showWarningMessage(err.message);
        return;
      }
      void vscode.window.showErrorMessage(
        vscode.l10n.t('Forebrain did not start the turn: {0}', err instanceof Error ? err.message : String(err)),
      );
      this.deps.log(`command: ${err instanceof Error ? err.message : String(err)}`);
    }
  }

  /** The newest conversation of the folder's project, when the gateway knows the project. */
  private async recent(folder: string): Promise<string | undefined> {
    const projects = await this.deps
      .client()
      .getJson<ProjectsAnswer>('/api/v1/projects', this.deps.token());
    const project = projectForFolder(projects.projects ?? [], folder);
    if (!project) return undefined;
    const sessions = await this.deps
      .client()
      .getJson<SessionsAnswer>(`/api/chat/sessions?project_id=${encodeURIComponent(project.id)}`, this.deps.token());
    return newestSession(sessions.records ?? []);
  }

  private async create(folder: string, uri: vscode.Uri): Promise<string> {
    // No `source`: the gateway's whitelist holds only "" and "workshop".
    const created = await this.deps.client().postJson<{ id: string }>('/api/chat/sessions', this.deps.token(), {
      title: sessionTitle(uri.path.split('/').pop() ?? uri.fsPath),
      folder,
    });
    return created.id;
  }
}