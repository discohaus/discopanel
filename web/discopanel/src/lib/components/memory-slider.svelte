<script lang="ts">
	import { untrack } from 'svelte';
	import { Label } from '$lib/components/ui/label';
	import { Link, Unlink } from '@lucide/svelte';

	interface Props {
		memory: number;
		memoryMin: number;
		memoryMax: number;
		totalMb?: number;
		occupiedMb?: number;
		disabled?: boolean;
		label?: string;
		dirty?: boolean;
	}

	let {
		memory = $bindable(),
		memoryMin = $bindable(),
		memoryMax = $bindable(),
		totalMb = 0,
		occupiedMb = 0,
		disabled = false,
		label = 'Memory',
		dirty = false
	}: Props = $props();

	const MIN_CONTAINER = 1024;
	const HEAP_FLOOR = 512;
	const STEP = 256;
	const CHIP_W = 56;
	const CHIP_GAP = 6;

	type Thumb = 'container' | 'max' | 'min';

	function snap(mb: number): number {
		return Math.round(mb / STEP) * STEP;
	}

	// Mirrors the backend heap defaults exactly
	function defaultHeap(containerMb: number): { min: number; max: number } {
		return {
			min: Math.floor(containerMb / 2),
			max: Math.floor((containerMb * 3) / 4)
		};
	}

	function isCustomHeap(): boolean {
		if (!memoryMin || !memoryMax) return false;
		const heap = defaultHeap(memory);
		return memoryMin !== heap.min || memoryMax !== heap.max;
	}

	// Custom values from the server start unlocked
	let unlocked = $state(isCustomHeap());
	let trackEl = $state<HTMLDivElement | null>(null);
	let trackWidth = $state(0);
	let dragging = $state<Thumb | null>(null);

	// Falls back to sane range when memory unknown
	let rangeMax = $derived(
		totalMb > 0 ? totalMb : Math.max(32768, Math.ceil((memory * 2) / 1024) * 1024)
	);

	let occupiedPct = $derived(Math.min((occupiedMb / rangeMax) * 100, 100));
	let overCommitted = $derived(totalMb > 0 && occupiedMb + memory > totalMb);

	// Heap follows external memory changes, like modpack presets
	$effect(() => {
		const containerMb = memory;
		untrack(() => {
			if (unlocked) {
				if (memoryMax > containerMb) memoryMax = containerMb;
				if (memoryMin > memoryMax) memoryMin = memoryMax;
				return;
			}
			const heap = defaultHeap(containerMb);
			if (memoryMin !== heap.min) memoryMin = heap.min;
			if (memoryMax !== heap.max) memoryMax = heap.max;
		});
	});

	// Fans chips apart so stacked thumbs stay readable
	let chipX = $derived.by(() => {
		const w = trackWidth;
		const half = CHIP_W / 2;
		const gap = CHIP_W + CHIP_GAP;
		const xs = [memoryMin, memoryMax, memory].map((mb) => Math.min(mb / rangeMax, 1) * w);
		for (let i = 0; i < 3; i++) xs[i] = Math.min(Math.max(xs[i], half), w - half);
		for (let i = 1; i < 3; i++) xs[i] = Math.max(xs[i], xs[i - 1] + gap);
		xs[2] = Math.min(xs[2], w - half);
		for (let i = 1; i >= 0; i--) xs[i] = Math.min(xs[i], xs[i + 1] - gap);
		xs[0] = Math.max(xs[0], half);
		return { min: xs[0], max: xs[1], container: xs[2] };
	});

	// Keeps link clasp inside the track
	let linkX = $derived.by(() => {
		const mid = ((memoryMin + memory) / 2 / rangeMax) * trackWidth;
		return Math.min(Math.max(mid, 10), Math.max(trackWidth - 10, 10));
	});

	function fmtGb(mb: number): string {
		const gb = Math.round((mb / 1024) * 100) / 100;
		return `${gb} GB`;
	}

	function setContainer(mb: number) {
		memory = Math.min(Math.max(snap(mb), MIN_CONTAINER), rangeMax);
		if (unlocked) {
			// Heap rides along so it always fits the container
			memoryMax = Math.min(memoryMax, memory);
			memoryMin = Math.min(memoryMin, memoryMax);
		} else {
			const heap = defaultHeap(memory);
			memoryMin = heap.min;
			memoryMax = heap.max;
		}
	}

	function setHeapMax(mb: number) {
		memoryMax = Math.min(Math.max(snap(mb), HEAP_FLOOR), memory);
		memoryMin = Math.min(memoryMin, memoryMax);
	}

	function setHeapMin(mb: number) {
		memoryMin = Math.min(Math.max(snap(mb), HEAP_FLOOR), memoryMax);
	}

	function applyValue(thumb: Thumb, mb: number) {
		if (thumb === 'container') setContainer(mb);
		else if (thumb === 'max') setHeapMax(mb);
		else setHeapMin(mb);
	}

	function valueOf(thumb: Thumb): number {
		if (thumb === 'container') return memory;
		return thumb === 'max' ? memoryMax : memoryMin;
	}

	function mbFromPointer(e: PointerEvent): number {
		if (!trackEl) return 0;
		const rect = trackEl.getBoundingClientRect();
		const ratio = Math.min(Math.max((e.clientX - rect.left) / rect.width, 0), 1);
		return ratio * rangeMax;
	}

	// Chips drag by delta so nudged chips never jump
	function startDrag(thumb: Thumb, jump = true) {
		return (e: PointerEvent) => {
			if (disabled) return;
			if (thumb !== 'container' && !unlocked) return;
			e.preventDefault();
			e.stopPropagation();
			dragging = thumb;
			const startX = e.clientX;
			const startMb = valueOf(thumb);
			if (jump) applyValue(thumb, mbFromPointer(e));

			const move = (ev: PointerEvent) => {
				if (jump) {
					applyValue(thumb, mbFromPointer(ev));
					return;
				}
				const rect = trackEl?.getBoundingClientRect();
				if (!rect || rect.width === 0) return;
				applyValue(thumb, startMb + ((ev.clientX - startX) / rect.width) * rangeMax);
			};
			const up = () => {
				dragging = null;
				window.removeEventListener('pointermove', move);
				window.removeEventListener('pointerup', up);
			};
			window.addEventListener('pointermove', move);
			window.addEventListener('pointerup', up);
		};
	}

	function handleTrackPointer(e: PointerEvent) {
		if (disabled) return;
		// Track clicks move the container thumb
		startDrag('container')(e);
	}

	function handleKeys(thumb: Thumb) {
		return (e: KeyboardEvent) => {
			if (disabled) return;
			let delta = 0;
			if (e.key === 'ArrowRight' || e.key === 'ArrowUp') delta = STEP;
			else if (e.key === 'ArrowLeft' || e.key === 'ArrowDown') delta = -STEP;
			else return;
			e.preventDefault();
			applyValue(thumb, valueOf(thumb) + delta * (e.shiftKey ? 4 : 1));
		};
	}

	function setUnlocked(checked: boolean) {
		unlocked = checked;
		if (!unlocked) {
			const heap = defaultHeap(memory);
			memoryMin = heap.min;
			memoryMax = heap.max;
		}
	}

	function pct(mb: number): number {
		return Math.min((mb / rangeMax) * 100, 100);
	}
</script>

{#snippet chip(thumb: Thumb, mb: number, x: number, caption: string, active: boolean)}
	<div
		class="absolute top-0 flex w-14 -translate-x-1/2 flex-col items-center leading-tight transition-opacity {active
			? 'cursor-ew-resize'
			: 'pointer-events-none opacity-45'}"
		style="left: {x}px"
		onpointerdown={active ? startDrag(thumb, false) : undefined}
	>
		<span
			class="tabular text-[11px] whitespace-nowrap {thumb === 'container'
				? 'font-semibold'
				: 'font-medium'} {dragging === thumb
				? 'text-primary'
				: thumb === 'container'
					? 'text-foreground'
					: 'text-muted-foreground'}"
		>
			{fmtGb(mb)}
		</span>
		<span class="text-[9px] tracking-wide whitespace-nowrap text-muted-foreground/70 uppercase">
			{caption}
		</span>
	</div>
{/snippet}

<div class="space-y-2">
	<div class="flex items-center gap-1.5">
		<Label class="text-xs font-medium text-muted-foreground">{label}</Label>
		{#if dirty}
			<span class="size-1.5 rounded-full bg-status-busy" title="Unsaved change"></span>
		{/if}
	</div>

	<div class="touch-none select-none {disabled ? 'opacity-50' : ''}">
		<div class="relative h-8" aria-hidden="true">
			{#if trackWidth > 0}
				{@render chip('min', memoryMin, chipX.min, 'Java min', !disabled && unlocked)}
				{@render chip('max', memoryMax, chipX.max, 'Java max', !disabled && unlocked)}
				{@render chip('container', memory, chipX.container, 'Server', !disabled)}
			{/if}
		</div>

		<div
			bind:this={trackEl}
			bind:clientWidth={trackWidth}
			role="presentation"
			class="relative h-8 {disabled ? '' : 'cursor-pointer'}"
			onpointerdown={handleTrackPointer}
		>
			<div class="absolute inset-x-0 top-1/2 h-1.5 -translate-y-1/2 rounded-full bg-muted">
				{#if totalMb > 0 && occupiedMb > 0}
					<div
						class="occupied-zone absolute inset-y-0 right-0 rounded-r-full"
						class:over={overCommitted}
						style="width: {occupiedPct}%"
					></div>
				{/if}
				<div
					class="absolute inset-y-0 left-0 rounded-full bg-primary/25"
					style="width: {pct(memory)}%"
				></div>
				<div
					class="absolute inset-y-0 rounded-full bg-primary/60"
					style="left: {pct(memoryMin)}%; width: {Math.max(pct(memoryMax) - pct(memoryMin), 0)}%"
				></div>
			</div>

			<button
				type="button"
				role="slider"
				aria-label="Initial Java memory (Xms)"
				aria-valuemin={HEAP_FLOOR}
				aria-valuemax={memoryMax}
				aria-valuenow={memoryMin}
				aria-valuetext={fmtGb(memoryMin)}
				tabindex={unlocked && !disabled ? 0 : -1}
				class="absolute top-1/2 size-3 -translate-1/2 rounded-full border shadow-sm ring-ring/50 transition-[box-shadow,opacity,background-color] focus-visible:ring-4 focus-visible:outline-hidden {unlocked
					? 'border-primary bg-primary hover:ring-4'
					: 'pointer-events-none border-primary/50 bg-background opacity-40'} {dragging === 'min'
					? 'ring-4'
					: ''}"
				style="left: {pct(memoryMin)}%"
				onpointerdown={startDrag('min')}
				onkeydown={handleKeys('min')}
				disabled={disabled || !unlocked}
				title="Initial Java memory (Xms) {fmtGb(memoryMin)}"
			></button>

			<button
				type="button"
				role="slider"
				aria-label="Max Java memory (Xmx)"
				aria-valuemin={HEAP_FLOOR}
				aria-valuemax={memory}
				aria-valuenow={memoryMax}
				aria-valuetext={fmtGb(memoryMax)}
				tabindex={unlocked && !disabled ? 0 : -1}
				class="absolute top-1/2 size-3 -translate-1/2 rounded-full border shadow-sm ring-ring/50 transition-[box-shadow,opacity,background-color] focus-visible:ring-4 focus-visible:outline-hidden {unlocked
					? 'border-primary bg-primary hover:ring-4'
					: 'pointer-events-none border-primary/50 bg-background opacity-40'} {dragging === 'max'
					? 'ring-4'
					: ''}"
				style="left: {pct(memoryMax)}%"
				onpointerdown={startDrag('max')}
				onkeydown={handleKeys('max')}
				disabled={disabled || !unlocked}
				title="Max Java memory (Xmx) {fmtGb(memoryMax)}"
			></button>

			<button
				type="button"
				role="slider"
				aria-label="Server memory"
				aria-valuemin={MIN_CONTAINER}
				aria-valuemax={rangeMax}
				aria-valuenow={memory}
				aria-valuetext={fmtGb(memory)}
				tabindex={disabled ? -1 : 0}
				class="absolute top-1/2 size-4.5 -translate-1/2 rounded-full border-2 border-primary bg-background shadow-sm ring-ring/50 transition-[box-shadow] hover:ring-4 focus-visible:ring-4 focus-visible:outline-hidden {dragging ===
				'container'
					? 'ring-4'
					: ''}"
				style="left: {pct(memory)}%"
				onpointerdown={startDrag('container')}
				onkeydown={handleKeys('container')}
				{disabled}
				title="Server memory {fmtGb(memory)}"
			></button>
		</div>

		<div class="relative h-6">
			<span class="absolute top-1/2 left-0 -translate-y-1/2 text-[10px] text-muted-foreground/70">
				0
			</span>
			<span class="absolute top-1/2 right-0 -translate-y-1/2 text-[10px] text-muted-foreground/70">
				{totalMb > 0 ? `${Math.round(rangeMax / 1024)} GB` : fmtGb(rangeMax)}
			</span>
			{#if trackWidth > 0}
				<div
					class="absolute top-1/2 {unlocked
						? 'border-t border-dashed border-muted-foreground/30'
						: 'h-px bg-primary/40'}"
					style="left: {pct(memoryMin)}%; width: {Math.max(pct(memory) - pct(memoryMin), 0)}%"
					aria-hidden="true"
				></div>
				<button
					type="button"
					class="absolute top-1/2 flex size-5 -translate-1/2 items-center justify-center rounded-full border bg-background shadow-sm transition-colors focus-visible:ring-4 focus-visible:ring-ring/50 focus-visible:outline-hidden {unlocked
						? 'text-muted-foreground hover:text-foreground'
						: 'border-primary/40 text-primary'}"
					style="left: {linkX}px"
					onclick={() => setUnlocked(!unlocked)}
					{disabled}
					aria-pressed={!unlocked}
					aria-label="Link Java memory to server memory"
					title={unlocked
						? 'Relink Java memory to server memory'
						: 'Unlink Java memory for manual tuning'}
				>
					{#if unlocked}
						<Unlink class="size-3" />
					{:else}
						<Link class="size-3" />
					{/if}
				</button>
			{/if}
		</div>
	</div>

	{#if totalMb > 0 && occupiedMb > 0}
		{#if overCommitted}
			<p class="text-[10px] text-status-busy">
				Sharing {fmtGb(occupiedMb + memory - totalMb)} with other servers
			</p>
		{:else}
			<p class="text-[10px] text-muted-foreground/80">
				{fmtGb(occupiedMb)} reserved by other servers
			</p>
		{/if}
	{/if}

	{#if unlocked}
		<p class="text-[11px] text-status-busy">
			Manual sizing can destabilize the server. Relink to restore automatic sizing.
		</p>
	{/if}
</div>

<style>
	/* Hatch marks memory reserved by other servers */
	.occupied-zone {
		background: repeating-linear-gradient(
			-45deg,
			color-mix(in oklch, var(--muted-foreground) 45%, transparent) 0 2px,
			transparent 2px 5px
		);
	}
	.occupied-zone.over {
		background: repeating-linear-gradient(
			-45deg,
			color-mix(in oklch, var(--status-busy) 70%, transparent) 0 2px,
			transparent 2px 5px
		);
	}
</style>
