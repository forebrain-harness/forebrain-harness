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

  constructor(private readonly session: () => string | undefined = () => undefined) {
    this.item.command = 'forebrain.chat.focus';
    this.item.name = 'Forebrain';
  }

  update(state: SessionState, connected: boolean): void {
    let text = '$(debug-disconnect) Forebrain';
    if (connected) text = state.runActive ? '$(sync~spin) Forebrain' : '$(radio-tower) Forebrain';
    const progress = checklistProgress(state);
    if (progress) text += ` $(check-all) ${progress.done}/${progress.total}`;
    if (state.pending.length > 0) text += ` $(bell) ${state.pending.length}`;
    this.item.text = text;

    const lines = [connected ? 'Forebrain: connected' : 'Forebrain: not connected'];
    const session = this.session();
    if (session) lines.push(`Session: ${session}`);
    if (state.runActive) lines.push('A turn is running');
    if (state.toolsInFlight > 0) lines.push(`Tools in flight: ${state.toolsInFlight}`);
    if (state.subagents > 0) lines.push(`Subagents: ${state.subagents}`);
    if (progress) {
      lines.push(`Checklist: ${progress.done}/${progress.total}${progress.active ? ` — ${progress.active}` : ''}`);
    }
    if (state.pending.length > 0) lines.push(`Approvals waiting: ${state.pending.length}`);
    this.item.tooltip = lines.join('\n');
    this.item.show();
  }

  dispose(): void {
    this.item.dispose();
  }
}