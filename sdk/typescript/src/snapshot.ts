/**
 * Holds a client's latest data and notifies listeners when it changes. Data
 * is compared by its JSON text, so an unchanged payload (after a reconnect or
 * a poll) is neither parsed nor reported.
 */
export class Snapshot<T> {
  value: T;
  #raw = '';
  readonly #listeners = new Set<() => void>();

  constructor(initial: T) {
    this.value = initial;
  }

  /** The JSON text of the value, or '' before the first set. */
  get raw(): string {
    return this.#raw;
  }

  /**
   * Replaces the value with the one raw (JSON text) holds, unless raw is
   * unchanged. It reports whether the value changed.
   */
  setRaw(raw: string, parse: (raw: string) => T = JSON.parse): boolean {
    if (raw === this.#raw) return false;
    this.value = parse(raw);
    this.#raw = raw;
    this.notify();
    return true;
  }

  /** Replaces the value with already-parsed data, unless it is unchanged. It reports whether the value changed. */
  set(value: T): boolean {
    return this.setRaw(JSON.stringify(value), () => value);
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
