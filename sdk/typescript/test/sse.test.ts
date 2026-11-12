import { describe, expect, it } from 'vitest';
import { readEvents, type ServerSentEvent } from '../src/sse.js';

function stream(chunks: string[]): ReadableStream<Uint8Array> {
  const enc = new TextEncoder();
  return new ReadableStream({
    start(controller) {
      for (const c of chunks) controller.enqueue(enc.encode(c));
      controller.close();
    },
  });
}

async function collect(chunks: string[]): Promise<ServerSentEvent[]> {
  const events: ServerSentEvent[] = [];
  await readEvents(stream(chunks), (e) => events.push(e), 1000);
  return events;
}

describe('readEvents', () => {
  it('parses named events, skipping comments', async () => {
    expect(await collect(['event: flags\ndata: {"a":1}\n\n: ping\n\nevent: flags\ndata: {"a":2}\n\n'])).toEqual([
      { event: 'flags', data: '{"a":1}' },
      { event: 'flags', data: '{"a":2}' },
    ]);
  });

  it('reports each event its own id', async () => {
    expect(await collect(['event: flags\nid: 7\ndata: a\n\nid: 8\n\nevent: flags\ndata: b\n\nid:9\ndata: c\n\n'])).toEqual([
      { event: 'flags', data: 'a', id: '7' },
      // an id on an event without data is dropped with it
      { event: 'flags', data: 'b' },
      { event: 'message', data: 'c', id: '9' },
    ]);
  });

  it('handles chunks split anywhere, CRLF, and multi-line data', async () => {
    const text = 'event: chamber\r\ndata: one\r\ndata:two\r\n\r\ndata: plain\n\n';
    const chunks = [...text]; // one character per chunk
    expect(await collect(chunks)).toEqual([
      { event: 'chamber', data: 'one\ntwo' },
      { event: 'message', data: 'plain' },
    ]);
  });

  it('decodes UTF-8 split across chunks', async () => {
    const bytes = new TextEncoder().encode('data: üser\n\n');
    const body = new ReadableStream<Uint8Array>({
      start(c) {
        c.enqueue(bytes.slice(0, 7));
        c.enqueue(bytes.slice(7));
        c.close();
      },
    });
    const events: ServerSentEvent[] = [];
    await readEvents(body, (e) => events.push(e), 1000);
    expect(events).toEqual([{ event: 'message', data: 'üser' }]);
  });

  it('rejects when the stream goes quiet', async () => {
    const body = new ReadableStream<Uint8Array>({ start() {} });
    await expect(readEvents(body, () => {}, 20)).rejects.toThrow(/no data/);
  });
});
