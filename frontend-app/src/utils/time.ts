import { getTimeZones } from '@vvo/tzdb';

function pad(n: number): string {
  return String(n).padStart(2, '0');
}

function formatDateComponents(date: Date): string {
  return (
    `${date.getFullYear()}-${pad(date.getMonth() + 1)}-${pad(date.getDate())} ` +
    `${pad(date.getHours())}:${pad(date.getMinutes())}:${pad(date.getSeconds())}`
  );
}

/** Full `YYYY-MM-DD HH:mm:ss` in the given IANA timezone (History column). */
export function formatTimestamp(timestamp: number, timezone: string): string {
  const date = new Date(timestamp);
  const zoned = new Date(date.toLocaleString('en-US', { timeZone: timezone }));
  return formatDateComponents(zoned);
}

/** `HH:mm` in the given timezone (dashboard line-chart x-axis labels). */
export function formatZonedTimestamp(
  timestamp: number,
  timezone: string,
): string {
  const formatter = new Intl.DateTimeFormat('en-US', {
    timeZone: timezone,
    hour12: false,
    hour: '2-digit',
    minute: '2-digit',
  });
  return formatter.format(new Date(timestamp));
}

/** Full date-time from an ISO string in the given timezone (dashboard "Started at"). */
export function formatZonedDateTime(
  isoDateTime: string,
  timezone: string,
): string {
  if (!isoDateTime) return '';
  const date = new Date(isoDateTime);
  const zoned = new Date(date.toLocaleString('en-US', { timeZone: timezone }));
  return formatDateComponents(zoned);
}

/** All IANA timezone names (incl. UTC), sorted alphabetically. */
export function getTimeZoneOptions(): string[] {
  return getTimeZones({ includeUtc: true })
    .map((tz) => tz.name)
    .sort();
}

/** Browser timezone, falling back to UTC. */
export function defaultTimezone(): string {
  try {
    return Intl.DateTimeFormat().resolvedOptions().timeZone ?? 'UTC';
  } catch {
    return 'UTC';
  }
}
