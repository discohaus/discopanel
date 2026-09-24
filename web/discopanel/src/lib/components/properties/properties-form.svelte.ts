import { SvelteMap, SvelteSet } from 'svelte/reactivity';
import type { PropertyCategory, ServerProperty } from '$lib/proto/discopanel/v1/properties_pb';

const SKIP_KEYS = ['id', 'serverId', 'updatedAt'];

export function categorySlug(name: string): string {
	return name.toLowerCase().replace(/\s+/g, '-');
}

// Tracks edits against loaded property values
export class PropertiesForm {
	categories = $state<PropertyCategory[]>([]);
	values = new SvelteMap<string, string | null>();
	enabled = new SvelteSet<string>();
	private original = new SvelteMap<string, string | null>();
	private originalEnabled = new SvelteSet<string>();
	private forServer: boolean;

	constructor(forServer: boolean) {
		this.forServer = forServer;
	}

	// Server mode keeps hidden system props editable
	get visibleCategories(): PropertyCategory[] {
		return this.categories
			.map((cat) => ({
				...cat,
				properties: this.forServer ? cat.properties : cat.properties.filter((p) => !p.system)
			}))
			.filter((cat) => cat.properties.length > 0);
	}

	process(categories: PropertyCategory[]) {
		this.categories = categories;
		this.original.clear();
		this.values.clear();
		this.originalEnabled.clear();
		this.enabled.clear();

		for (const category of categories) {
			for (const prop of category.properties) {
				if (SKIP_KEYS.includes(prop.key)) continue;

				const value = prop.value || null;
				this.original.set(prop.key, value);
				this.values.set(prop.key, value);

				const hasValue = value !== null && value !== '';
				const shouldEnable = this.forServer
					? hasValue || prop.required || prop.system
					: hasValue || prop.required;

				if (shouldEnable) {
					this.originalEnabled.add(prop.key);
					this.enabled.add(prop.key);
				}
			}
		}
	}

	isModified(key: string): boolean {
		const wasEnabled = this.originalEnabled.has(key);
		const isEnabled = this.enabled.has(key);
		if (wasEnabled !== isEnabled) return true;
		return isEnabled && this.values.get(key) !== this.original.get(key);
	}

	readonly modifiedKeys = $derived.by(() => {
		const modified = new SvelteSet<string>();
		for (const key of this.values.keys()) {
			if (this.isModified(key)) modified.add(key);
		}
		return modified;
	});

	readonly modifiedCountBySlug = $derived.by(() => {
		const counts = new SvelteMap<string, number>();
		for (const cat of this.visibleCategories) {
			let count = 0;
			for (const prop of cat.properties) {
				if (this.modifiedKeys.has(prop.key)) count++;
			}
			counts.set(categorySlug(cat.name), count);
		}
		return counts;
	});

	get dirty(): boolean {
		return this.modifiedKeys.size > 0;
	}

	// Unset fields save as empty to clear them
	buildUpdates(): Record<string, string> {
		const updates: Record<string, string> = {};
		for (const key of this.modifiedKeys) {
			updates[key] = this.enabled.has(key) ? (this.values.get(key) ?? '') : '';
		}
		return updates;
	}

	reset() {
		this.values.clear();
		for (const [key, value] of this.original) this.values.set(key, value);
		this.enabled.clear();
		for (const key of this.originalEnabled) this.enabled.add(key);
	}

	toggle(key: string, on: boolean, prop: ServerProperty) {
		if (on) {
			this.enabled.add(key);
			if (!this.values.get(key)) {
				this.values.set(key, prop.defaultValue ?? defaultForType(prop.type));
			}
		} else {
			this.enabled.delete(key);
		}
	}

	setValue(key: string, value: string | boolean) {
		const strValue = typeof value === 'boolean' ? String(value) : value;
		this.values.set(key, strValue || null);
	}

	displayValue(prop: ServerProperty): string {
		const value = this.values.get(prop.key);
		if (this.enabled.has(prop.key) && value !== null && value !== undefined) {
			return value;
		}
		return prop.defaultValue ?? '';
	}

	boolValue(prop: ServerProperty): boolean {
		return this.displayValue(prop).toLowerCase() === 'true';
	}
}

function defaultForType(type: string): string {
	switch (type) {
		case 'number':
			return '0';
		case 'checkbox':
			return 'false';
		default:
			return '';
	}
}
