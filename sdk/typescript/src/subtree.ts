// The chambers of a subtree, as `GET /v1/chambers/<root>?subtree=true` and
// its event stream serve them (see docs/design/subtree.md).

import { isRecord } from './snapshot.js';
import { parseRevision, RealmError, type Feed } from './transport.js';
import type { Chamber } from './types.js';

/** The inherited view of one chamber of a subtree, as the server built it. */
export interface View {
  readonly chamber: Chamber;
  /** The server's revision of the view (see parseRevision), if it reported one. */
  readonly revision: number | undefined;
  // The view's rules as JSON text, which tells an unchanged view.
  readonly raw: string;
}

/**
 * Holds the views of every chamber in a subtree, by path, and notifies
 * listeners when they change. It is the Feed a ServerClient with a path
 * template follows: a read replaces every view; an event stream sends every
 * view, then `synced`, then each change. A view the server could not build
 * (an entry with an `error`) keeps the one held for its path, if any. Data
 * that is not a subtree (from a server that does not support subtree reads,
 * say) is refused and never replaces a view.
 */
export class Subtree implements Feed {
  readonly query = 'subtree=true';
  #views = new Map<string, View>();
  #loaded = false;
  readonly #listeners = new Set<() => void>();
  // The snapshot an event stream is sending, from its start until its first
  // synced: each path's view, or undefined for one the server could not
  // build.
  #batch: Map<string, View | undefined> | undefined;
  // The paths a stream has sent since its last synced, after its first.
  readonly #seen = new Set<string>();
  // Whether an event since the start of the stream or its last synced could
  // not be read far enough to tell its path: then no path is known to be
  // gone, and synced drops none.
  #unknown = false;

  /** Whether a whole subtree has arrived from the server. */
  get loaded(): boolean {
    return this.#loaded;
  }

  /** The view of the chamber at path (canonical, such as `/prod/`), if one is held. */
  get(path: string): View | undefined {
    return this.#views.get(path);
  }

  /** Calls listener whenever the views change. Returns a function that unsubscribes. */
  subscribe(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }

  /**
   * Replaces every view with those of a subtree read's data (its
   * `chambers`). It throws, keeping the views, when data is not a subtree.
   */
  set(data: unknown): void {
    if (!isRecord(data) || !Array.isArray(data.chambers)) throw notSubtree();
    const entries = new Map<string, View | undefined>();
    for (const item of data.chambers) {
      const path = entryPath(item);
      if (path === undefined) throw notSubtree();
      const revision = isRecord(item) ? revisionOf(item.revision) : undefined;
      entries.set(path, entryView(item, revision));
    }
    this.#replace(entries, true);
  }

  /** Starts a new event stream, which first sends the whole subtree. */
  open(): void {
    this.#batch = new Map();
    this.#seen.clear();
    this.#unknown = false;
  }

  /**
   * Receives an event of a subtree stream. It returns true for one that
   * brings the views up to date: `synced`, and each change after the
   * stream's first `synced`. It throws for an event it cannot use, keeping
   * the view held for its path.
   */
  event(name: string, raw: string, revision: number | undefined): boolean {
    switch (name) {
      case 'chamber': {
        const { path, data } = this.#read(raw);
        let view: View | undefined;
        try {
          view = entryView(data, revision);
        } catch (e) {
          // Its path stays as it was.
          this.#sent(path, undefined);
          throw e;
        }
        this.#sent(path, view);
        if (this.#batch) return false;
        if (view) {
          // An unchanged view may come at another revision.
          const changed = this.#views.get(path)?.raw !== view.raw;
          this.#views.set(path, view);
          if (changed) this.#notify();
        }
        return true;
      }
      case 'deleted': {
        const { path } = this.#read(raw);
        if (this.#batch) {
          this.#batch.delete(path);
          return false;
        }
        this.#seen.delete(path);
        if (this.#views.delete(path)) this.#notify();
        return true;
      }
      case 'synced': {
        if (this.#batch) {
          const batch = this.#batch;
          this.#batch = undefined;
          this.#replace(batch, !this.#unknown);
        } else if (!this.#unknown) {
          // A resync: the whole subtree again, applied as it arrived.
          let dropped = false;
          for (const path of [...this.#views.keys()]) {
            if (!this.#seen.has(path)) dropped = this.#views.delete(path) || dropped;
          }
          if (dropped) this.#notify();
        }
        this.#seen.clear();
        this.#unknown = false;
        return true;
      }
      default:
        // newer servers may send other events
        return false;
    }
  }

  // read parses the data of a chamber or deleted event, returning its path.
  // It throws when the data is not JSON or has no path, noting that a path
  // went unread.
  #read(raw: string): { path: string; data: unknown } {
    let data: unknown;
    try {
      data = JSON.parse(raw);
    } catch (e) {
      this.#unknown = true;
      throw e;
    }
    const path = entryPath(data);
    if (path === undefined) {
      this.#unknown = true;
      throw new RealmError('the server sent an event without a chamber path; it may not support subtree watches');
    }
    return { path, data };
  }

  // sent records that the stream sent path, with view (undefined: keep the
  // view held for it).
  #sent(path: string, view: View | undefined): void {
    if (this.#batch) this.#batch.set(path, view);
    else this.#seen.add(path);
  }

  // replace makes entries the views, keeping the view held for a path whose
  // entry is undefined (the server could not build it) and, unless
  // complete, for paths entries leaves out; it notifies listeners of any
  // change, and of the first load.
  #replace(entries: Map<string, View | undefined>, complete: boolean): void {
    const previous = this.#views;
    const next = new Map<string, View>();
    let changed = !this.#loaded;
    for (const [path, entry] of entries) {
      const held = previous.get(path);
      const view = entry ?? held;
      if (!view) continue;
      next.set(path, view);
      if (held?.raw !== view.raw) changed = true;
    }
    for (const [path, view] of previous) {
      if (next.has(path)) continue;
      if (complete) changed = true;
      else next.set(path, view);
    }
    this.#views = next;
    this.#loaded = true;
    if (changed) this.#notify();
  }

  // notify calls every listener. A listener that throws is reported to
  // console.error; it neither stops the others nor reaches the caller.
  #notify(): void {
    for (const listener of this.#listeners) {
      try {
        listener();
      } catch (e) {
        console.error('[realm] a subscriber threw', e);
      }
    }
  }
}

function notSubtree(): RealmError {
  return new RealmError('the server did not return a subtree; it may not support subtree reads');
}

// entryPath returns the path of a subtree entry (an element of a read's
// chambers, or the data of an event), or undefined when it has none that is
// canonical: one that starts and ends with `/`.
function entryPath(entry: unknown): string | undefined {
  if (!isRecord(entry)) return undefined;
  const path = entry.path;
  return typeof path === 'string' && path.startsWith('/') && path.endsWith('/') ? path : undefined;
}

// entryView returns the view in a subtree entry, at revision, or undefined
// for an entry the server could not build (one with an `error`). It throws
// for an entry with neither.
function entryView(entry: unknown, revision: number | undefined): View | undefined {
  if (isRecord(entry) && isRecord(entry.rules)) {
    const chamber = { rules: entry.rules } as unknown as Chamber;
    return { chamber, revision, raw: JSON.stringify(entry.rules) };
  }
  if (isRecord(entry) && typeof entry.error === 'string') return undefined;
  throw new RealmError(`the server did not return a chamber for ${entryPath(entry) ?? 'a path'}`);
}

// revisionOf reads the revision of a subtree read's entry, as parseRevision
// reads a header.
function revisionOf(value: unknown): number | undefined {
  return typeof value === 'number' || typeof value === 'string' ? parseRevision(String(value)) : undefined;
}
