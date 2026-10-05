// Deterministic banded column layout keeps poll rebuilds stable

// Minimal shape zone math needs from any node
export interface ZoneItem {
	id: string;
	column: number;
	height: number;
}

export interface LayoutItem extends ZoneItem {
	order: number;
	band: number;
	group?: string;
	indent?: boolean;
}

export interface LayoutEdge {
	source: string;
	target: string;
}

// Columns are internet router listeners services backends
export const COLUMN_X = [20, 332, 644, 956, 1300];
export const COLUMN_W = [224, 224, 224, 256, 240];
const NODE_GAP = 18;
const BAND_GAP = 64;
const INDENT_X = 28;
const ZONE_PAD = 20;
const ZONE_HEADER = 44;

// Positions nodes into columns split by band
export function layoutColumns(
	items: LayoutItem[],
	edges: LayoutEdge[]
): Map<string, { x: number; y: number }> {
	const positions = new Map<string, { x: number; y: number }>();

	const byColumn = new Map<number, LayoutItem[]>();
	for (const item of items) {
		const list = byColumn.get(item.column) ?? [];
		list.push(item);
		byColumn.set(item.column, list);
	}

	const heights = new Map(items.map((i) => [i.id, i.height]));
	const xFor = (item: LayoutItem): number =>
		(COLUMN_X[item.column] ?? item.column * 270) + (item.indent ? INDENT_X : 0);

	// Mean center of placed neighbors on one side
	const meanCenter = (id: string, incoming: boolean): number | null => {
		let sum = 0;
		let count = 0;
		for (const edge of edges) {
			const own = incoming ? edge.target : edge.source;
			const other = incoming ? edge.source : edge.target;
			if (own !== id) continue;
			const pos = positions.get(other);
			if (!pos) continue;
			sum += pos.y + (heights.get(other) ?? 0) / 2;
			count++;
		}
		return count === 0 ? null : sum / count;
	};

	// Stacks a list, pulling each node level with its neighbors
	const place = (
		list: LayoutItem[],
		startY: number,
		want: (item: LayoutItem) => number | null
	): number => {
		let y = startY;
		for (const item of list) {
			const center = want(item);
			const at = center === null ? y : Math.max(y, center - item.height / 2);
			positions.set(item.id, { x: xFor(item), y: at });
			y = at + item.height + NODE_GAP;
		}
		return Math.max(startY, y - NODE_GAP);
	};

	const alignToSources = (item: LayoutItem) => meanCenter(item.id, true);

	// First band places left to right so edges never cross
	const bandHeights = new Map<number, number>();
	for (const column of [...byColumn.keys()].sort((a, b) => a - b)) {
		if (column === 4) continue;
		const list = byColumn.get(column) ?? [];
		list.sort((a, b) => a.band - b.band || a.order - b.order || a.id.localeCompare(b.id));
		const bandZero = list.filter((i) => i.band === 0);
		if (column >= 3) {
			const means = new Map(
				bandZero.map((i) => [i.id, meanCenter(i.id, true) ?? Number.MAX_VALUE])
			);
			bandZero.sort((a, b) => (means.get(a.id) ?? 0) - (means.get(b.id) ?? 0) || a.order - b.order);
		}
		bandHeights.set(column, place(bandZero, 0, alignToSources));
	}

	// Backends order by grouped barycenter of their sources
	interface Group {
		key: string;
		members: LayoutItem[];
		band: number;
		mean: number;
	}
	const backends = byColumn.get(4) ?? [];
	const groups: Group[] = [];
	if (backends.length > 0) {
		const byGroup = new Map<string, Group>();
		for (const item of backends) {
			const key = item.group ?? item.id;
			const g = byGroup.get(key) ?? { key, members: [], band: 9, mean: Number.MAX_VALUE };
			g.members.push(item);
			g.band = Math.min(g.band, item.band);
			byGroup.set(key, g);
		}
		// Group center follows every feeder of its members
		for (const group of byGroup.values()) {
			const centers = group.members
				.map((m) => meanCenter(m.id, true))
				.filter((c): c is number => c !== null);
			if (centers.length > 0) {
				group.mean = centers.reduce((a, b) => a + b, 0) / centers.length;
			}
		}
		groups.push(
			...[...byGroup.values()].sort(
				(a, b) => a.band - b.band || a.mean - b.mean || a.key.localeCompare(b.key)
			)
		);
		let y = 0;
		let done = false;
		for (const group of groups) {
			if (group.band > 0 && !done) {
				done = true;
				bandHeights.set(4, Math.max(0, y - NODE_GAP));
			}
			group.members.sort(
				(a, b) => Number(a.indent ?? false) - Number(b.indent ?? false) || a.order - b.order
			);
			// Whole group rides level with what feeds it
			const block =
				group.members.reduce((sum, m) => sum + m.height, 0) + NODE_GAP * (group.members.length - 1);
			let top = group.mean === Number.MAX_VALUE ? y : Math.max(y, group.mean - block / 2);
			for (const item of group.members) {
				positions.set(item.id, { x: COLUMN_X[4] + (item.indent ? INDENT_X : 0), y: top });
				top += item.height + NODE_GAP;
			}
			y = top;
		}
		if (!done) bandHeights.set(4, Math.max(0, y - NODE_GAP));
	}

	// Trunk nodes center against everything they feed
	for (const column of [1, 0]) {
		const list = (byColumn.get(column) ?? []).filter((i) => i.band === 0);
		if (list.length === 0) continue;
		bandHeights.set(
			column,
			place(list, 0, (item) => meanCenter(item.id, false))
		);
	}

	// Second band starts below the tallest first band
	const bandZeroMax = Math.max(0, ...bandHeights.values());
	for (const column of [...byColumn.keys()].sort((a, b) => a - b)) {
		if (column === 4) continue;
		const bandOne = (byColumn.get(column) ?? []).filter((i) => i.band > 0);
		if (bandOne.length > 0) place(bandOne, bandZeroMax + BAND_GAP, alignToSources);
	}

	// Lower band backends shift under the same divider
	let shift = 0;
	for (const group of groups) {
		if (group.band === 0) continue;
		for (const item of group.members) {
			const pos = positions.get(item.id);
			if (!pos) continue;
			if (shift === 0 && pos.y < bandZeroMax + BAND_GAP) {
				shift = bandZeroMax + BAND_GAP - pos.y;
			}
			if (shift > 0) positions.set(item.id, { x: pos.x, y: pos.y + shift });
		}
	}

	return positions;
}

export interface ZoneRect {
	x: number;
	y: number;
	width: number;
	height: number;
}

// Bounds one zone band around its member columns
export function zoneRect(
	columns: number[],
	items: ZoneItem[],
	positions: Map<string, { x: number; y: number }>,
	top: number,
	bottom: number
): ZoneRect | null {
	let minX = Number.MAX_VALUE;
	let maxX = -Number.MAX_VALUE;
	let found = false;
	for (const item of items) {
		if (!columns.includes(item.column)) continue;
		const pos = positions.get(item.id);
		if (!pos) continue;
		found = true;
		minX = Math.min(minX, pos.x);
		maxX = Math.max(maxX, pos.x + (COLUMN_W[item.column] ?? 224));
	}
	if (!found) {
		// Empty zones still draw at their column slot
		const col = columns[0];
		minX = COLUMN_X[col] ?? 0;
		maxX = minX + (COLUMN_W[col] ?? 224);
	}
	return {
		x: minX - ZONE_PAD,
		y: top - ZONE_HEADER,
		width: maxX - minX + ZONE_PAD * 2,
		height: bottom - top + ZONE_HEADER + ZONE_PAD
	};
}

// Shared vertical extent across every zone band
export function contentBounds(
	items: ZoneItem[],
	positions: Map<string, { x: number; y: number }>
): { top: number; bottom: number } {
	let top = 0;
	let bottom = 0;
	for (const item of items) {
		const pos = positions.get(item.id);
		if (!pos) continue;
		top = Math.min(top, pos.y);
		bottom = Math.max(bottom, pos.y + item.height);
	}
	return { top, bottom };
}
