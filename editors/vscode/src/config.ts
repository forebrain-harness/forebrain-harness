import * as vscode from 'vscode';
import { parseGatewayOrigin, type GatewayConfig } from './gatewayOrigin';

export function readGatewayConfig(config: vscode.WorkspaceConfiguration = vscode.workspace.getConfiguration('forebrain')): GatewayConfig {
  return {
    origin: parseGatewayOrigin(config.get<string>('gatewayUrl', 'http://127.0.0.1:6060')),
    binaryPath: (config.get<string>('binaryPath', '') ?? '').trim(),
    autoStartGateway: config.get<boolean>('autoStartGateway', true),
    relayLogRequests: config.get<boolean>('relayLogRequests', false),
  };
}