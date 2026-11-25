// What a ServerClient follows and evaluates against: the chamber at one path
// (ChamberSource), or the subtree below a path template's root that the
// template picks a chamber from for each context (SubtreeSource). The client
// chooses one when it is constructed; nothing else in it depends on which. The
// Go SDK has the same seam (pkg/snapshot.go).

import { isRecord, Snapshot } from './snapshot.js';
import { Subtree } from './subtree.js';
import { resolvePath, type PathTemplate } from './template.js';
import { RealmError, sinkFeed, type Feed } from './transport.js';
import type { Chamber, EvaluationContext } from './types.js';

/** The chamber a context is evaluated against. */
export interface Target {
  /** The chamber's path, such as `/prod/tenants/t-42/`. */
  readonly path: string;
  readonly chamber: Chamber;
  /** The server's revision of the chamber, if it reported one. */
  readonly revision: number | undefined;
}

/** What a ServerClient follows and evaluates against. */
export interface Source {
  /** The path the client reads and watches: the chamber's, or the template's root. */
  readonly readPath: string;
  /** What the transport delivers the server's data to. */
  readonly feed: Feed;
  /** Whether the server's data has arrived. */
  loaded(): boolean;
  /** Calls listener whenever what the client evaluates against changes. Returns a function that unsubscribes. */
  subscribe(listener: () => void): () => void;
  /**
   * The chamber context is evaluated against, or undefined when there is none
   * yet (or, with a template, context resolves to none). defaults fill in for
   * the attributes context does not set where they pick the chamber.
   */
  pick(context: EvaluationContext, defaults: Record<string, string>): Target | undefined;
}

/** The chamber at one path. */
export class ChamberSource implements Source {
  readonly readPath: string;
  readonly #chamber = new Snapshot<Chamber>({ rules: {} }, asChamber);
  readonly feed: Feed = sinkFeed('chamber', this.#chamber);

  /** path is the chamber's, as given to the client; it is made canonical. */
  constructor(path: string | undefined) {
    const segments = (path ?? '/').split('/').filter((s) => s !== '');
    this.readPath = segments.length === 0 ? '/' : `/${segments.join('/')}/`;
  }

  loaded(): boolean {
    return this.#chamber.raw !== '';
  }

  subscribe(listener: () => void): () => void {
    return this.#chamber.subscribe(listener);
  }

  pick(): Target | undefined {
    if (!this.loaded()) return undefined;
    return { path: this.readPath, chamber: this.#chamber.value, revision: this.#chamber.revision };
  }
}

/** The subtree below a path template's root, which the template picks a chamber from for each context. */
export class SubtreeSource implements Source {
  readonly readPath: string;
  readonly #template: PathTemplate;
  readonly #subtree = new Subtree();
  readonly feed: Feed = this.#subtree;

  constructor(template: PathTemplate) {
    this.#template = template;
    this.readPath = template.root;
  }

  loaded(): boolean {
    return this.#subtree.loaded;
  }

  subscribe(listener: () => void): () => void {
    return this.#subtree.subscribe(listener);
  }

  pick(context: EvaluationContext, defaults: Record<string, string>): Target | undefined {
    const subtree = this.#subtree;
    const path = resolvePath(this.#template, (p) => subtree.get(p) !== undefined, context, defaults);
    const view = path === undefined ? undefined : subtree.get(path);
    return path === undefined || !view ? undefined : { path, chamber: view.chamber, revision: view.revision };
  }
}

// asChamber returns data as a chamber, or throws when it is not one: a
// gateway or maintenance page in front of the server may answer with other
// JSON.
function asChamber(data: unknown): Chamber {
  if (!isRecord(data) || !isRecord(data.rules)) throw new RealmError('the server did not return a chamber');
  return data as unknown as Chamber;
}
