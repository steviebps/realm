/**
 * Holds a client's latest data and notifies listeners when it changes. Data
 * is compared by its JSON text, so an unchanged payload (after a reconnect or
 * a poll) is neither parsed nor reported. Data that fails the check (a
 * gateway's `{}`, say) never replaces the value.
 */
export class Snapshot<T> {
  value: T;
  #revision: number | undefined;
  #raw = '';
  readonly #check: (data: unknown) => T;
  readonly #listeners = new Set<() => void>();

  /** check returns data as a T, or throws when it is not one. */
  constructor(initial: T, check: (data: unknown) => T) {
    this.value = initial;
    this.#check = check;
  }

  /**
   * The server's revision of the value (see parseRevision), or undefined
   * when it reported none or the value did not come from it.
   */
  get revision(): number | undefined {
    return this.#revision;
  }

  /** The JSON text of the value, or '' before the first set. */
  get raw(): string {
    return this.#raw;
  }

  /**
   * Replaces the value with the one raw (JSON text) holds, at revision,
   * unless raw is unchanged: then only the revision is replaced, and
   * listeners are not told. It reports whether the value changed, and
   * throws, keeping the value and revision, when raw does not parse or its
   * data fails the check.
   */
  setRaw(raw: string, revision?: number, parse: (raw: string) => unknown = JSON.parse): boolean {
    if (raw === this.#raw) {
      this.#revision = revision;
      return false;
    }
    this.value = this.#check(parse(raw));
    this.#raw = raw;
    this.#revision = revision;
    this.notify();
    return true;
  }

  /**
   * Replaces the value with already-parsed data, at revision, as setRaw
   * does. It throws, keeping the value and revision, when the data fails
   * the check.
   */
  set(data: unknown, revision?: number): boolean {
    const value = this.#check(data);
    return this.setRaw(JSON.stringify(value), revision, () => value);
  }

  /**
   * Calls every listener. A listener that throws is reported to
   * console.error; it neither stops the others nor reaches the caller, which
   * may be reading an event stream.
   */
  notify(): void {
    for (const listener of this.#listeners) {
      try {
        listener();
      } catch (e) {
        console.error('[realm] a subscriber threw', e);
      }
    }
  }

  /** Calls listener on every change. Returns a function that unsubscribes. */
  subscribe(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }
}

/** Reports whether value is a JSON object: not null, an array, or a primitive. */
export function isRecord(value: unknown): value is Record<string, unknown> {
  return typeof value === 'object' && value !== null && !Array.isArray(value);
}
