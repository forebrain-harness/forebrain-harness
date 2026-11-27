import * as vscode from 'vscode';
import { ChatPanel } from './panel';
import { readyFrameFor, type PanelState } from './panelHtml';
import { Connection } from './connection';
import { readGatewayConfig } from './config';
import { ConfigError } from './gatewayOrigin';
import { GatewayClient } from './gateway';
import { findForebrainBinary, startGateway } from './gatewayProcess';
import { redact } from './token';
import { EMPTY_SESSION_STATE, reduceSessionMessage, reconcilePending, type SessionState } from './sessionState';
import { SessionSocket, type ServerMessage } from './sessionSocket';
import { StatusBar } from './statusBar';
import { Approvals } from './approvals';
import { Deliverables } from './deliverables';
import { ForebrainCommands } from './commands';
import { WebviewRpc } from './webviewRpc';

// Module level so that `deactivate` can reach them: the subscription below
// disposes them too, but a shutdown closes the socket rather than racing it.
let active: Connection | undefined;
let link: SessionSocket | undefined;
/** The host socket's link state: what the status bar's "not connected" reads. */
let linkUp = false;

/**
 * The secondary sidebar became a public contribution point in 1.106 (1.105
 * still gated it behind a proposed-API check). Below that the chat view falls
 * back to the activity bar — the same capability key the manifest's `when`
 * clauses read, so exactly one of the two chat containers is ever shown.
 */
function supportsSecondarySidebar(): boolean {
  const [major, minor] = vscode.version.split('.').map((part) => Number.parseInt(part, 10));
  if (!Number.isFinite(major) || !Number.isFinite(minor)) return false;
  return major > 1 || (major === 1 && minor >= 106);
}

export async function activate(context: vscode.ExtensionContext): Promise<void> {
  const output = vscode.window.createOutputChannel('Forebrain');
  const log = (line: string) => output.appendLine(`${new Date().toISOString()} ${line}`);

  const secondary = supportsSecondarySidebar();
  await vscode.commands.executeCommand('setContext', 'forebrain.doesNotSupportSecondarySidebar', !secondary);
  await vscode.commands.executeCommand('setContext', 'forebrain.sessionsListEnabled', true);
  // Focus follows the capability, never visibility: the chat view that exists
  // on this VS Code is the one the command opens.
  const chatViewId = secondary ? 'forebrain.chatSecondary' : 'forebrain.chatSidebar';
  const focusChat = () => void vscode.commands.executeCommand(`${chatViewId}.focus`);

  /** The conversation the webview last bound — the panel's current session. */
  let observed: string | undefined;
  const statusBar = new StatusBar(() => link?.boundSession ?? observed, `${chatViewId}.focus`);
  // The fold of everything the host socket receives: one state, the surfaces.
  const state: { current: SessionState } = { current: EMPTY_SESSION_STATE };

  const reflect = (previous: SessionState, next: SessionState): void => {
    statusBar.update(next, linkUp);
    approvals.onState(previous, next, chatPanel.visible);
    deliverables.onRunEnd(previous, next);
  };

  /** A different conversation: the old one's approvals do not belong to it. */
  const reset = (): void => {
    const previous = state.current;
    state.current = EMPTY_SESSION_STATE;
    reflect(previous, state.current);
  };

  const listener = (msg: ServerMessage): void => {
    const previous = state.current;
    state.current = reduceSessionMessage(state.current, msg);
    if (state.current !== previous) reflect(previous, state.current);
  };

  const client = () => new GatewayClient(active!.origin);
  const token = () => active?.currentToken() ?? '';

  const approvals = new Approvals({
    client,
    token,
    boundSession: () => link?.boundSession,
    notificationsEnabled: () => vscode.workspace.getConfiguration('forebrain').get<boolean>('approvalNotifications', true),
    focusPanel: focusChat,
    reconciled: (pendingIds) => {
      const previous = state.current;
      state.current = reconcilePending(state.current, pendingIds);
      if (state.current !== previous) reflect(previous, state.current);
    },
    log,
  });

  const deliverables = new Deliverables({ client, token, boundSession: () => link?.boundSession, log });

  const commands = new ForebrainCommands({
    workspaceState: context.workspaceState,
    connected: () => Boolean(active?.currentToken()),
    token,
    client,
    observedSession: () => observed,
    showSession: async (id) => {
      if (!active) throw new Error(vscode.l10n.t('the panel is not connected'));
      await active.showSession(id);
    },
    startTurn: async (sessionId, content) => {
      if (!link) throw new Error(vscode.l10n.t('the gateway link is not up'));
      if (link.boundSession !== sessionId) await link.bind(sessionId, 0);
      return link.startRun(sessionId, content);
    },
    focusPanel: focusChat,
    log,
  });

  const onConnectionState = (panelState: PanelState): void => {
    if (panelState.kind === 'ready' && !link && active) {
      const socket = new SessionSocket({
        origin: active.origin,
        getToken: () => active?.currentToken() ?? '',
        log,
        listener,
        onLink: (up) => {
          linkUp = up;
          statusBar.update(state.current, up);
        },
      });
      link = socket;
      // A new socket knows no session: the fallback bind is the only thing that
      // can put it on the conversation the panel is showing.
      void socket
        .open()
        .then(async () => {
          if (observed !== undefined && socket.boundSession !== observed) await socket.bind(observed, 0);
        })
        .catch((err: unknown) => log(`session socket: ${err instanceof Error ? err.message : String(err)}`));
      return;
    }
    if (panelState.kind !== 'ready' && link) {
      const closing = link;
      link = undefined;
      linkUp = false;
      reset();
      void closing.close();
    }
  };

  // The three views of the alignment contract: the chat workbench in two
  // containers (one app, shown by capability) and the Sessions list. Each holds
  // its own RPC dispatcher; the host socket follows whichever conversation the
  // chat view binds.
  const distRoot = () => vscode.Uri.joinPath(context.extensionUri, 'webview', 'dist');
  /** A session a view asked for while the target view was still loading: the
   *  webview announces itself with a `ready` frame, and the pending pick goes
   *  out then. A rebuild lands on the conversation it had. */
  const pendingSession: { chat?: string; sessions?: string } = {};
  const pushThemeTo = (webview: vscode.Webview): void => {
    const kind = vscode.window.activeColorTheme.kind;
    void webview.postMessage({
      type: 'theme',
      dark: kind === vscode.ColorThemeKind.Dark || kind === vscode.ColorThemeKind.HighContrast,
    });
  };

  const setPanelState = (panelState: PanelState): void => {
    chatPanel.setState(panelState);
    sessionsPanel.setState(panelState);
    onConnectionState(panelState);
  };

  const wireView = (view: vscode.WebviewView, entry: 'index' | 'sessions'): void => {
    const rpc = new WebviewRpc({
      post: (message) => void view.webview.postMessage(message),
      origin: active?.origin ?? '',
      token: () => active?.currentToken() ?? '',
      fetchImpl: (input, init) => fetch(input, init),
      log: (l) => log(redact(l, active?.currentToken() ?? '')),
      workspaceRoots: () => (vscode.workspace.workspaceFolders ?? []).map((folder) => folder.uri.fsPath),
      workspaceTrusted: () => vscode.workspace.isTrusted,
      trustSet: async (root, trusted) => {
        await new GatewayClient(active?.origin ?? '').postJson('/api/host/trust', active?.currentToken() ?? '', { path: root, trusted });
      },
      showWarningModal: async (message, detail, confirmLabel, cancelLabel) =>
        vscode.window.showWarningMessage(message, { modal: true, detail }, confirmLabel, cancelLabel),
      bindSession: async (sessionId) => {
        // The webview bound a conversation: the host socket follows it there.
        observed = sessionId;
        reset();
        const socket = link;
        if (socket && socket.boundSession !== sessionId) await socket.bind(sessionId, 0);
      },
      openChatSession: async (sessionId) => {
        if (sessionId) pendingSession.chat = sessionId;
        focusChat();
      },
      reportActiveSession: async (sessionId) => {
        pendingSession.sessions = sessionId;
        void vscode.commands.executeCommand('forebrain.sessionsList.focus');
      },
      openDeliverable: async (path) => {
        await deliverables.openArtifact(path);
      },
      openPlanDoc: async (path) => {
        const uri = vscode.Uri.file(path);
        if (path.toLowerCase().endsWith('.md')) {
          await vscode.commands.executeCommand('markdown.showPreview', uri);
        } else {
          await vscode.window.showTextDocument(uri, { preview: false });
        }
      },
      openWebUi: async (path) => {
        openWebUiAt(path);
      },
      openFileRange: (request) => deliverables.openRange(request),
    });
    view.webview.onDidReceiveMessage((m: unknown) => {
      // The app announces itself once its transport is installed; anything a
      // view asked for while it loaded goes out now — and the chat view that
      // comes back after a rebuild lands on the conversation it had, so the
      // re-bind and the history reload replay the snapshot.
      if ((m as { type?: unknown })?.type === 'ready') {
        const frame = readyFrameFor(entry, pendingSession, observed);
        if (frame) {
          if (entry === 'index') pendingSession.chat = undefined;
          else pendingSession.sessions = undefined;
          void view.webview.postMessage(frame);
        }
      }
      rpc.onMessage(m);
    });
    const themeSubscription = vscode.window.onDidChangeActiveColorTheme(() => pushThemeTo(view.webview));
    view.onDidDispose(() => {
      rpc.dispose();
      themeSubscription.dispose();
    });
    context.subscriptions.push(themeSubscription);
    pushThemeTo(view.webview);
  };

  const chatPanel = new ChatPanel(distRoot, 'index', (view) => wireView(view, 'index'));
  const sessionsPanel = new ChatPanel(distRoot, 'sessions', (view) => wireView(view, 'sessions'));

  /** One page of the gateway's own Web UI, in the person's browser. The
   *  sign-in link the gateway's banner prints carries the token in the
   *  fragment (the Web UI reads it once and wipes it); `redirect` lands the
   *  reader on the page they asked for. The token leaves the extension only
   *  into that browser, the way the banner already does. */
  const openWebUiAt = (path: string): void => {
    if (!active) return;
    const current = active.currentToken();
    if (!current) { void vscode.window.showInformationMessage(vscode.l10n.t('Forebrain: connect the panel first.')); return; }
    const target = path && path !== '/' ? `?redirect=${encodeURIComponent(path)}` : '';
    void vscode.env.openExternal(vscode.Uri.parse(`${active.origin}/login${target}#token=${encodeURIComponent(current)}`));
  };

  const build = (): Connection => {
    const config = readGatewayConfig(); // throws ConfigError: handled by the caller
    const probeClient = new GatewayClient(config.origin);
    return new Connection({
      config, env: process.env,
      // async 包一层：context.secrets.* 返回 vscode.Thenable（PromiseLike，不是 Promise），
      // 直接赋给 ConnectionDeps/SecretStore 的 Promise 签名会 typecheck 失败（TS2739/TS2322）。
      secrets: {
        get: async (k) => context.secrets.get(k),
        store: async (k, v) => { await context.secrets.store(k, v); },
        delete: async (k) => { await context.secrets.delete(k); },
      },
      probe: (t) => probeClient.probe(t), healthy: () => probeClient.health(),
      startGateway: async () => {
        const bin = await findForebrainBinary(config.binaryPath);
        if (!bin) {
          log('gateway: no forebrain binary found (set forebrain.binaryPath or install @forebrain-harness/forebrain)');
          return null;
        }
        log(`gateway: starting ${bin}`);
        try {
          const p = await startGateway([bin, 'gateway', 'start'], log);
          log(`gateway: started (pid ${p.pid}) at ${p.origin}`);
          return p;
        } catch (err) {
          // startGateway never throws the token (Step 5); a port already taken
          // or a failing binary lands here and is reported, not retried.
          log(`gateway: failed to start: ${err instanceof Error ? err.message : String(err)}`);
          return null;
        }
      },
      log: (l) => log(redact(l, active?.currentToken() ?? '')),
      emit: setPanelState,
    });
  };

  const boot = async (): Promise<void> => {
    try {
      active ??= build();
      await active.start();
    } catch (err) {
      const detail = err instanceof ConfigError ? err.message : err instanceof Error ? err.message : String(err);
      log(`startup failed: ${detail}`);
      // Through the same door as every other state: an active connection left
      // behind here would keep a socket retrying with an empty token.
      setPanelState({ kind: 'unavailable', reason: 'error', detail });
    }
  };

  context.subscriptions.push(
    output,
    statusBar,
    approvals,
    vscode.window.registerWebviewViewProvider('forebrain.chatSidebar', chatPanel, { webviewOptions: { retainContextWhenHidden: true } }),
    vscode.window.registerWebviewViewProvider('forebrain.chatSecondary', chatPanel, { webviewOptions: { retainContextWhenHidden: true } }),
    vscode.window.registerWebviewViewProvider('forebrain.sessionsList', sessionsPanel, { webviewOptions: { retainContextWhenHidden: true } }),
    vscode.commands.registerCommand('forebrain.openPanel', focusChat),
    vscode.commands.registerCommand('forebrain.reconnect', () => void active?.reconnect()),
    vscode.commands.registerCommand('forebrain.askAboutSelection', () => commands.askAboutSelection()),
    vscode.commands.registerCommand('forebrain.askAboutFile', (uri?: vscode.Uri) => commands.askAboutFile(uri)),
    vscode.commands.registerCommand('forebrain.openDeliveredFile', () => deliverables.openDeliveredFile()),
    vscode.commands.registerCommand('forebrain.enterGatewayToken', async () => {
      const entered = await vscode.window.showInputBox({ prompt: vscode.l10n.t('Forebrain gateway token'), password: true, ignoreFocusOut: true });
      if (entered === undefined) return;
      if (!active) {
        void vscode.window.showInformationMessage(vscode.l10n.t('Forebrain: not connected yet. Run the reconnect command first.'));
        return;
      }
      const verdict = await active.enterToken(entered);
      if (verdict === 'rejected') void vscode.window.showErrorMessage(vscode.l10n.t('Forebrain: the gateway rejected that token.'));
      else if (verdict === 'unreachable') void vscode.window.showWarningMessage(vscode.l10n.t('Forebrain: the gateway is not answering.'));
    }),
    vscode.commands.registerCommand('forebrain.openWebUiInBrowser', () => {
      openWebUiAt('/');
    }),
    vscode.workspace.onDidChangeConfiguration((e) => {
      if (e.affectsConfiguration('forebrain.gatewayUrl') || e.affectsConfiguration('forebrain.binaryPath') || e.affectsConfiguration('forebrain.autoStartGateway')) {
        // A new origin/binary needs a new Connection; the old one may own a
        // gateway child, so it is disposed, not dropped.
        const previous = active;
        active = undefined;
        void (async () => {
          await previous?.dispose();
          await boot();
        })();
      }
    }),
    chatPanel.onCommand((id) => void vscode.commands.executeCommand(id)),
    sessionsPanel.onCommand((id) => void vscode.commands.executeCommand(id)),
    {
      dispose: () => {
        // `Connection.dispose()` emits nothing, so nothing else would ever
        // close the host socket.
        void link?.close();
        link = undefined;
        linkUp = false;
        void active?.dispose();
      },
    },
  );
  // Before the first event arrives: the status bar needs its first line.
  statusBar.update(state.current, false);
  await boot();
}

export async function deactivate(): Promise<void> {
  await link?.close();
  link = undefined;
  linkUp = false;
  await active?.dispose();
  active = undefined;
}