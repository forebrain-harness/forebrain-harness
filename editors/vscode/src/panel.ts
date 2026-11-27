import * as vscode from 'vscode';
import { PANEL_COMMANDS, renderPanelHtml, renderWebviewHtml, type PanelState } from './panelHtml';

/**
 * One native webview view of the alignment contract: the chat workbench (two
 * containers, one app, shown by capability) or the Sessions list. While the host
 * holds no gateway it shows the connection-state document; once ready it loads
 * the built app, which reaches the gateway only through the host's RPC proxy.
 * `wire` attaches that proxy and the theme push once, when the view resolves.
 */
export class ChatPanel implements vscode.WebviewViewProvider {
  private view?: vscode.WebviewView;
  private state: PanelState = { kind: 'connecting' };
  private readonly onCommandEmitter = new vscode.EventEmitter<string>();
  readonly onCommand = this.onCommandEmitter.event;

  constructor(
    private readonly distRoot: () => vscode.Uri,
    private readonly entry: 'index' | 'sessions',
    private readonly wire: (view: vscode.WebviewView) => void,
  ) {}

  resolveWebviewView(view: vscode.WebviewView): void {
    this.view = view;
    view.webview.options = { enableScripts: true, localResourceRoots: [this.distRoot()] };
    view.webview.html = this.html(view);
    view.webview.onDidReceiveMessage((m: unknown) => {
      const type = (m as { type?: unknown })?.type;
      const id = (m as { type?: unknown; id?: unknown })?.id;
      if (type === 'command' && typeof id === 'string' && PANEL_COMMANDS.has(id)) {
        this.onCommandEmitter.fire(id);
      }
    });
    this.wire(view);
  }

  private html(view: vscode.WebviewView): string {
    return this.state.kind === 'ready'
      ? renderWebviewHtml(view.webview, this.distRoot(), this.entry)
      : renderPanelHtml(this.state);
  }

  /** Whether the panel is on screen: an approval notification is only worth showing when it is not. */
  get visible(): boolean {
    return this.view?.visible ?? false;
  }

  /** Replaces the document: the connection state changed, so what loads changes. */
  setState(state: PanelState): void {
    this.state = state;
    if (this.view) this.view.webview.html = this.html(this.view);
  }
}