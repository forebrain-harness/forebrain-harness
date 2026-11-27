import * as vscode from 'vscode';
import { PANEL_COMMANDS, renderPanelHtml, type PanelState } from './panelHtml';

export class ChatPanel implements vscode.WebviewViewProvider {
  private view?: vscode.WebviewView;
  private state: PanelState = { kind: 'connecting' };
  private readonly onCommandEmitter = new vscode.EventEmitter<string>();
  readonly onCommand = this.onCommandEmitter.event;
  private readonly onFileRangeEmitter = new vscode.EventEmitter<unknown>();
  /** A file range the Web UI asked to open; the shape is the gate's to check. */
  readonly onFileRange = this.onFileRangeEmitter.event;

  resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view;
    view.webview.options = { enableScripts: true, localResourceRoots: [] };
    view.webview.html = renderPanelHtml(this.state);
    view.webview.onDidReceiveMessage((m: unknown) => {
      const type = (m as { type?: unknown })?.type;
      const id = (m as { type?: unknown; id?: unknown })?.id;
      if (type === 'command' && typeof id === 'string' && PANEL_COMMANDS.has(id)) {
        this.onCommandEmitter.fire(id);
      } else if (type === 'openFileRange') {
        this.onFileRangeEmitter.fire(m);
      }
    });
  }

  /** Whether the panel is on screen: an approval notification is only worth showing when it is not. */
  get visible(): boolean {
    return this.view?.visible ?? false;
  }

  /** Replaces the document: the relay origin changed, so the iframe must. */
  setState(state: PanelState): void {
    this.state = state;
    if (this.view) this.view.webview.html = renderPanelHtml(state);
  }
}