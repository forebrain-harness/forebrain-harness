// The status bar line: whether the host holds a link, whether a turn is
// running, how far the checklist is, and whether an approval is waiting.
//
// The link state comes first because it is the one that makes the rest stale:
// the `Connection` stays ready when the gateway process dies, so only the
// socket knows the line is down.
import * as vscode from 'vscode';
import { checklistProgress, type SessionState } from './sessionState';

export class StatusBar {
  private readonly item = vscode.window.createStatusBarItem('forebrain', vscode.StatusBarAlignment.Left, 100);

  constructor(
    private readonly session: () => string | undefined = () => undefined,
    focusCommand = 'forebrain.chatSecondary.focus',
  ) {
    this.item.command = focusCommand;
    this.item.name = 'Forebrain';
  }

  update(state: SessionState, connected: boolean): void {
    let text = '$(debug-disconnect) Forebrain';
    if (connected) text = state.runActive ? '$(sync~spin) Forebrain' : '$(radio-tower) Forebrain';
    const progress = checklistProgress(state);
    if (progress) text += ` $(check-all) ${progress.done}/${progress.total}`;
    if (state.pending.length > 0) text += ` $(bell) ${state.pending.length}`;
    this.item.text = text;

    const lines = [connected ? vscode.l10n.t('Forebrain: connected') : vscode.l10n.t('Forebrain: not connected')];
    const session = this.session();
    if (session) lines.push(vscode.l10n.t('Session: {0}', session));
    if (state.runActive) lines.push(vscode.l10n.t('A turn is running'));
    if (state.toolsInFlight > 0) lines.push(vscode.l10n.t('Tools in flight: {0}', state.toolsInFlight));
    if (state.subagents > 0) lines.push(vscode.l10n.t('Subagents: {0}', state.subagents));
    if (progress) {
      lines.push(progress.active
        ? vscode.l10n.t('Checklist: {0}/{1} — {2}', progress.done, progress.total, progress.active)
        : vscode.l10n.t('Checklist: {0}/{1}', progress.done, progress.total));
    }
    if (state.pending.length > 0) lines.push(vscode.l10n.t('Approvals waiting: {0}', state.pending.length));
    this.item.tooltip = lines.join('\n');
    this.item.show();
  }

  dispose(): void {
    this.item.dispose();
  }
}