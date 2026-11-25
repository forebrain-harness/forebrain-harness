// Native approval notifications.
//
// The panel's own card is where an approval is answered in full — rules,
// prefixes and all. This is the doorway for when the panel is not on screen:
// one notification with Approve and Deny, decided over REST (the receipt of an
// `approve_action` op goes back only to the connection that sent it, so the
// socket could never learn the outcome).
//
// Nothing tells an observer connection that an approval was answered
// elsewhere, so a pending list is reconciled against the gateway every few
// seconds. That list is also the only thing the bell icon in the status bar
// can trust: a decision made in the panel, in a terminal or in another window
// shows up here as the id disappearing.
import * as vscode from 'vscode';
import { approvalDetail, approvalHeadline } from './approvalText';
import type { PendingApproval, SessionState } from './sessionState';

const RECONCILE_MS = 4_000;

/** One row of `GET /api/actions?status=pending&session_id=…`. */
interface PendingAction {
  readonly id: string;
}

export interface ApprovalDeps {
  /** Constructed on demand: the gateway origin is only known once a connection exists. */
  readonly client: () => { getJson<T>(path: string, token: string): Promise<T>; postJson<T>(path: string, token: string, body: unknown): Promise<T> };
  readonly token: () => string;
  readonly boundSession: () => string | undefined;
  readonly notificationsEnabled: () => boolean;
  readonly focusPanel: () => void;
  /** The pending ids the gateway still knows — the state owner drops the rest. */
  readonly reconciled: (pendingIds: readonly string[]) => void;
  readonly log: (line: string) => void;
}

export class Approvals {
  private timer?: NodeJS.Timeout;
  private disposed = false;

  constructor(private readonly deps: ApprovalDeps) {}

  /** After every fold: announce what is new, and keep the reconcile timer honest. */
  onState(previous: SessionState, next: SessionState, panelVisible: boolean): void {
    const known = new Set(previous.pending.map((p) => p.actionId));
    for (const pending of next.pending) {
      if (known.has(pending.actionId)) continue;
      // With the panel on screen the card is already in front of the user; a
      // notification on top of it would only be a second one.
      if (!panelVisible && this.deps.notificationsEnabled()) void this.announce(pending);
    }
    this.schedule(next.pending.length > 0);
  }

  dispose(): void {
    this.disposed = true;
    this.stop();
  }

  private async announce(pending: PendingApproval): Promise<void> {
    const headline = approvalHeadline(pending);
    const detail = approvalDetail(pending.permissionInput);
    const text = detail ? `${headline}\n${detail}` : headline;
    // Only a tool approval can be answered with two buttons here; a question
    // (`user_interaction` and the like) needs the card in the panel.
    const choice = pending.toolName
      ? await vscode.window.showWarningMessage(text, 'Approve', 'Deny', 'Open panel')
      : await vscode.window.showInformationMessage(text, 'Open panel');
    if (choice === 'Open panel') {
      this.deps.focusPanel();
      return;
    }
    if (choice === undefined) return; // dismissed: the card in the panel still stands
    await this.decide(pending.actionId, choice === 'Approve' ? 'accept' : 'deny');
  }

  private async decide(actionId: string, decision: 'accept' | 'deny'): Promise<void> {
    const verb = decision === 'accept' ? 'approve' : 'deny';
    try {
      await this.deps.client().postJson(`/api/actions/${encodeURIComponent(actionId)}/${verb}`, this.deps.token(), {
        decision,
      });
    } catch (err) {
      void vscode.window.showErrorMessage(
        `Forebrain could not ${verb} that action: ${err instanceof Error ? err.message : String(err)}`,
      );
    }
  }

  private schedule(wanted: boolean): void {
    if (this.disposed) return;
    if (!wanted) {
      this.stop();
      return;
    }
    if (this.timer) return;
    this.timer = setInterval(() => void this.reconcile(), RECONCILE_MS);
  }

  private stop(): void {
    if (!this.timer) return;
    clearInterval(this.timer);
    this.timer = undefined;
  }

  private async reconcile(): Promise<void> {
    const session = this.deps.boundSession();
    const token = this.deps.token();
    if (!session || !token) return;
    try {
      const actions = await this.deps
        .client()
        .getJson<readonly PendingAction[]>(
          `/api/actions?status=pending&session_id=${encodeURIComponent(session)}&limit=50`,
          token,
        );
      this.deps.reconciled(actions.map((action) => action.id));
    } catch (err) {
      // A gateway that cannot answer leaves the list as it was; the next tick
      // asks again, and a reconnect clears it anyway.
      this.deps.log(`approvals: reconcile failed: ${err instanceof Error ? err.message : String(err)}`);
    }
  }
}