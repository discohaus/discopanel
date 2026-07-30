// Deterministic banded column layout keeps poll rebuilds stable

export interface LayoutItem {
	id: string;
	column: number;
	height: number;
	order: number;
	band: number;
	group?: string;
	indent?: boolean;
}

export interface LayoutEdge {
	source: string;
	target: string;
}

const COLUMN_X = [0, 268, 540, 762, 1080];
const NODE_GAP = 18;
const BAND_GAP = 64;
const INDENT_X = 28;

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

	const stack = (list: LayoutItem[], startY: number): number => {
		let y = startY;
		for (const item of list) {
			const x = (COLUMN_X[item.column] ?? item.column * 270) + (item.indent ? INDENT_X : 0);
			positions.set(item.id, { x, y });
			y += item.height + NODE_GAP;
		}
		return Math.max(startY, y - NODE_GAP);
	};

	// First band stacks per column in declared order
	const bandHeights = new Map<number, number>();
	for (const [column, list] of byColumn) {
		if (column === 4) continue;
		list.sort((a, b) => a.band - b.band || a.order - b.order || a.id.localeCompare(b.id));
		bandHeights.set(
			column,
			stack(
				list.filter((i) => i.band === 0),
				0
			)
		);
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
		const incoming = new Map<string, number[]>();
		for (const edge of edges) {
			const src = positions.get(edge.source);
			if (!src) continue;
			const list = incoming.get(edge.target) ?? [];
			list.push(src.y);
			incoming.set(edge.target, list);
		}
		const byGroup = new Map<string, Group>();
		for (const item of backends) {
			const key = item.group ?? item.id;
			const g = byGroup.get(key) ?? { key, members: [], band: 9, mean: Number.MAX_VALUE };
			g.members.push(item);
			g.band = Math.min(g.band, item.band);
			const ys = incoming.get(item.id);
			if (ys && ys.length > 0) {
				g.mean = Math.min(g.mean, ys.reduce((a, b) => a + b, 0) / ys.length);
			}
			byGroup.set(key, g);
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
			for (const item of group.members) {
				positions.set(item.id, { x: COLUMN_X[4] + (item.indent ? INDENT_X : 0), y });
				y += item.height + NODE_GAP;
			}
		}
		if (!done) bandHeights.set(4, Math.max(0, y - NODE_GAP));
	}

	// Second band starts below the tallest first band
	const bandZeroMax = Math.max(0, ...bandHeights.values());
	for (const [column, list] of byColumn) {
		if (column === 4) continue;
		const bandOne = list.filter((i) => i.band > 0);
		if (bandOne.length > 0) stack(bandOne, bandZeroMax + BAND_GAP);
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

	// Center short first bands against the tallest one
	for (const [column, list] of byColumn) {
		const height = bandHeights.get(column) ?? 0;
		const offset = (bandZeroMax - height) / 2;
		if (offset <= 0) continue;
		for (const item of list) {
			if (item.band > 0) continue;
			const pos = positions.get(item.id);
			if (pos) positions.set(item.id, { x: pos.x, y: pos.y + offset });
		}
	}

	return positions;
}
