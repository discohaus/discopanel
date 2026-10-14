<script lang="ts">
	import { cn } from '$lib/utils';

	let {
		name,
		favicon = '',
		size = 'md',
		class: className = ''
	}: {
		name: string;
		favicon?: string;
		size?: 'sm' | 'md' | 'lg' | 'xl';
		class?: string;
	} = $props();

	const SIZES = {
		sm: 'size-6 rounded-md text-xs',
		md: 'size-9 rounded-lg text-sm',
		lg: 'size-12 rounded-lg text-lg',
		xl: 'size-16 rounded-xl text-2xl'
	} as const;

	// Fixed tile palette keyed by name hash keeps avatars stable
	const HUES = [292, 340, 210, 160, 45, 265, 190, 25];

	function hashHue(input: string): number {
		let h = 0;
		for (let i = 0; i < input.length; i++) {
			h = (h * 31 + input.charCodeAt(i)) | 0;
		}
		return HUES[Math.abs(h) % HUES.length];
	}

	let src = $derived(
		favicon
			? favicon.startsWith('data:') || favicon.startsWith('http')
				? favicon
				: `data:image/png;base64,${favicon}`
			: ''
	);
	let hue = $derived(hashHue(name));
	let initial = $derived((name.trim()[0] || '?').toUpperCase());
	let broken = $state(false);

	$effect(() => {
		void src;
		broken = false;
	});
</script>

{#if src && !broken}
	<img
		{src}
		alt=""
		class={cn('pixelated shrink-0 border border-border/60 object-cover', SIZES[size], className)}
		onerror={() => (broken = true)}
	/>
{:else}
	<span
		class={cn(
			'inline-flex shrink-0 items-center justify-center border font-semibold select-none',
			SIZES[size],
			className
		)}
		style="background: oklch(0.45 0.14 {hue} / 0.18); border-color: oklch(0.6 0.14 {hue} / 0.35); color: oklch(0.72 0.14 {hue})"
	>
		{initial}
	</span>
{/if}
