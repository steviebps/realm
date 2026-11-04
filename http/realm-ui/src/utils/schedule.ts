import { Schedule } from '../models/response';

const pad = (n: number) => String(Math.abs(n)).padStart(2, '0');

// wallTime formats d as a datetime-local value in the viewer's time zone.
const wallTime = (d: Date) =>
  `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`;

/** Formats d as an RFC 3339 time carrying the viewer's offset, such as 2026-11-04T14:00:00-05:00. */
const withOffset = (d: Date) => {
  const offset = -d.getTimezoneOffset();
  return `${wallTime(d)}:${pad(d.getSeconds())}${offset < 0 ? '-' : '+'}${pad(Math.trunc(offset / 60))}:${pad(offset % 60)}`;
};

/** Converts an RFC 3339 time to a datetime-local input value in the viewer's time zone. */
export const toLocalInput = (time?: string): string => {
  const d = new Date(time ?? '');
  return Number.isNaN(d.getTime()) ? '' : wallTime(d);
};

/**
 * Converts a datetime-local input value, read in the viewer's time zone, to an
 * RFC 3339 time with that zone's offset. An empty value is undefined.
 */
export const fromLocalInput = (local: string): string | undefined => {
  const d = new Date(local);
  return local && !Number.isNaN(d.getTime()) ? withOffset(d) : undefined;
};

/** Formats an RFC 3339 time for display, in the viewer's time zone. */
export const formatTime = (time: string) =>
  new Date(time).toLocaleString(undefined, {
    weekday: 'short',
    month: 'short',
    day: 'numeric',
    hour: 'numeric',
    minute: '2-digit',
    timeZoneName: 'short',
  });

/** A one-hour schedule starting at the next full hour. */
export const nextHour = (): Schedule => {
  const start = new Date();
  start.setHours(start.getHours() + 1, 0, 0, 0);
  const end = new Date(start);
  end.setHours(end.getHours() + 1);
  return { start: withOffset(start), end: withOffset(end) };
};

/** Whether a schedule is yet to start, in effect, or ended at now. */
export const scheduleState = (schedule: Schedule, now = Date.now()): 'upcoming' | 'active' | 'ended' => {
  if (schedule.start && now < Date.parse(schedule.start)) return 'upcoming';
  if (schedule.end && now >= Date.parse(schedule.end)) return 'ended';
  return 'active';
};
