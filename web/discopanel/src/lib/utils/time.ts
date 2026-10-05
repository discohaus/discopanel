import type { Timestamp } from '@bufbuild/protobuf/wkt';

export function timestampToDate(ts: Timestamp | undefined): Date | null {
	if (!ts) return null;
	return new Date(Number(ts.seconds) * 1000 + (ts.nanos ?? 0) / 1_000_000);
}

// Short relative time like "3m ago" or "just now"
export function formatRelative(input: Timestamp | Date | undefined, now = new Date()): string {
	const date = input instanceof Date ? input : timestampToDate(input);
	if (!date) return 'never';
	const diff = now.getTime() - date.getTime();
	if (diff < 60_000) return 'just now';
	const minutes = Math.floor(diff / 60_000);
	if (minutes < 60) return `${minutes}m ago`;
	const hours = Math.floor(minutes / 60);
	if (hours < 24) return `${hours}h ago`;
	const days = Math.floor(hours / 24);
	if (days === 1) return 'yesterday';
	if (days < 7) return `${days}d ago`;
	return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric' });
}

// Compact elapsed duration like "2d 4h" or "12m"
export function formatUptime(input: Timestamp | Date | undefined, now = new Date()): string {
	const date = input instanceof Date ? input : timestampToDate(input);
	if (!date) return '--';
	const diff = Math.max(now.getTime() - date.getTime(), 0);
	const days = Math.floor(diff / 86_400_000);
	const hours = Math.floor((diff % 86_400_000) / 3_600_000);
	const minutes = Math.floor((diff % 3_600_000) / 60_000);
	if (days > 0) return `${days}d ${hours}h`;
	if (hours > 0) return `${hours}h ${minutes}m`;
	return `${minutes}m`;
}

export function formatDate(input: Timestamp | Date | undefined): string {
	const date = input instanceof Date ? input : timestampToDate(input);
	if (!date) return '--';
	return date.toLocaleDateString(undefined, { month: 'short', day: 'numeric', year: 'numeric' });
}

export function formatDateTime(input: Timestamp | Date | undefined): string {
	const date = input instanceof Date ? input : timestampToDate(input);
	if (!date) return '--';
	return date.toLocaleString(undefined, {
		month: 'short',
		day: 'numeric',
		year: 'numeric',
		hour: '2-digit',
		minute: '2-digit'
	});
}
