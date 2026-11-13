import { rpcClient } from '$lib/api/rpc-client';
import {
	ConfigFormat,
	type ConfigOverrideFile,
	type ConfigOverrideFileStatus
} from '$lib/proto/discopanel/v1/properties_pb';

export interface OverrideRow {
	key: string;
	value: string;
}

export interface OverrideFileRow {
	uid: number;
	path: string;
	format: ConfigFormat;
	entries: OverrideRow[];
}

let nextUid = 0;

function toRows(files: ConfigOverrideFile[]): OverrideFileRow[] {
	return files.map((f) => ({
		uid: nextUid++,
		path: f.path,
		format: f.format,
		entries: f.entries.map((e) => ({ key: e.key, value: e.value }))
	}));
}

// Stable text for one file, blank rows ignored like on save
function fileKey(f: OverrideFileRow): string {
	return JSON.stringify([
		f.path.trim(),
		f.format,
		f.entries.filter((e) => e.key.trim() !== '').map((e) => [e.key.trim(), e.value])
	]);
}

// Tracks config override edits against what the server stored
export class ConfigOverridesForm {
	files = $state<OverrideFileRow[]>([]);
	status = $state<ConfigOverrideFileStatus[]>([]);
	loading = $state(true);
	loadError = $state('');
	private baseline = $state<string[]>([]);

	dirtyCount = $derived.by(() => {
		if (this.loading || this.loadError) return 0;
		const current = this.files.map(fileKey);
		const added = current.filter((k) => !this.baseline.includes(k)).length;
		const removed = this.baseline.filter((k) => !current.includes(k)).length;
		return Math.max(added, removed);
	});

	private accept(files: ConfigOverrideFile[], status: ConfigOverrideFileStatus[]) {
		this.files = toRows(files);
		this.status = status;
		this.baseline = this.files.map(fileKey);
	}

	async load(serverId: string) {
		this.loading = true;
		this.loadError = '';
		try {
			const res = await rpcClient.properties.getConfigOverrides({ serverId });
			this.accept(res.files, res.status);
		} catch (error) {
			console.error(error);
			this.files = [];
			this.loadError = 'Failed to load config overrides';
		} finally {
			this.loading = false;
		}
	}

	async save(serverId: string) {
		const res = await rpcClient.properties.updateConfigOverrides({
			serverId,
			files: this.files.map((f) => ({
				path: f.path.trim(),
				// The extension decides the format, explicit values only narrow it
				format: ConfigFormat.UNSPECIFIED,
				entries: f.entries
					.filter((e) => e.key.trim() !== '')
					.map((e) => ({ key: e.key.trim(), value: e.value }))
			}))
		});
		this.accept(res.files, res.status);
	}

	reset() {
		const parsed = this.baseline.map((k) => JSON.parse(k) as [string, ConfigFormat, string[][]]);
		this.files = parsed.map(([path, format, entries]) => ({
			uid: nextUid++,
			path,
			format,
			entries: entries.map(([key, value]) => ({ key, value }))
		}));
	}

	addFile(path = '') {
		this.files.push({
			uid: nextUid++,
			path,
			format: ConfigFormat.UNSPECIFIED,
			entries: [{ key: '', value: '' }]
		});
	}

	removeFile(uid: number) {
		this.files = this.files.filter((f) => f.uid !== uid);
	}

	statusFor(path: string): ConfigOverrideFileStatus | undefined {
		return this.status.find((s) => s.path === path.trim());
	}
}
