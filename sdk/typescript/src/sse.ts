/** One server-sent event. */
export interface ServerSentEvent {
  event: string;
  data: string;
}

/**
 * Reads server-sent events from body until it ends, calling onEvent for each.
 * It rejects when no bytes (events or heartbeats) arrive for idleTimeout
 * milliseconds, so a silently dead connection is noticed.
 */
export async function readEvents(
  body: ReadableStream<Uint8Array>,
  onEvent: (event: ServerSentEvent) => void,
  idleTimeout: number,
): Promise<void> {
  const reader = body.getReader();
  const decoder = new TextDecoder();
  let buffer = '';
  let event = '';
  let data: string[] = [];

  const line = (l: string) => {
    if (l === '') {
      if (data.length > 0) onEvent({ event: event || 'message', data: data.join('\n') });
      event = '';
      data = [];
      return;
    }
    if (l[0] === ':') return; // comment, such as a heartbeat
    const colon = l.indexOf(':');
    const field = colon === -1 ? l : l.slice(0, colon);
    let value = colon === -1 ? '' : l.slice(colon + 1);
    if (value[0] === ' ') value = value.slice(1);
    if (field === 'event') event = value;
    else if (field === 'data') data.push(value);
  };

  try {
    for (;;) {
      const { value, done } = await withTimeout(reader.read(), idleTimeout);
      if (done) return;
      buffer += decoder.decode(value, { stream: true });
      for (;;) {
        const m = /\r\n|\n|\r/.exec(buffer);
        // A trailing "\r" may be the first half of a "\r\n" split across chunks.
        if (!m || (m[0] === '\r' && m.index === buffer.length - 1)) break;
        line(buffer.slice(0, m.index));
        buffer = buffer.slice(m.index + m[0].length);
      }
    }
  } finally {
    reader.cancel().catch(() => {});
  }
}

function withTimeout<T>(p: Promise<T>, ms: number): Promise<T> {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const timeout = new Promise<never>((_, reject) => {
    timer = setTimeout(() => reject(new Error(`no data from the event stream for ${ms} ms`)), ms);
  });
  return Promise.race([p, timeout]).finally(() => clearTimeout(timer));
}
