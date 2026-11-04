export { BrowserClient, type BrowserClientOptions } from './browser.js';
export { ServerClient, type ServerClientOptions } from './server.js';
export { RealmError, type ClientOptions } from './transport.js';
export { bucket, evaluateChamber, KEY_ATTRIBUTE, VERSION_ATTRIBUTE } from './evaluate.js';
export type {
  Chamber,
  Clause,
  EvaluatedFlag,
  EvaluatedFlags,
  EvaluationContext,
  Flag,
  FlagType,
  Operator,
  Rollout,
  Schedule,
  TargetingRule,
} from './types.js';
