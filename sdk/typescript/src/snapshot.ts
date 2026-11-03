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

  /** Replaces the value with the one raw (JSON text) holds, unless raw is unchanged. */
  setRaw(raw: string, parse: (raw: string) => T = JSON.parse): void {
    if (raw === this.#raw) return;
    this.value = parse(raw);
    this.#raw = raw;
    for (const listener of this.#listeners) listener();
  }

  /** Replaces the value with already-parsed data, unless it is unchanged. */
  set(value: T): void {
    this.setRaw(JSON.stringify(value), () => value);
  }

  /** Calls listener on every change. Returns a function that unsubscribes. */
  subscribe(listener: () => void): () => void {
    this.#listeners.add(listener);
    return () => this.#listeners.delete(listener);
  }
}
