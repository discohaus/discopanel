<script lang="ts">
	import { parseMotd } from '$lib/utils/motd';
	import { cn } from '$lib/utils';

	let {
		motd,
		class: className = ''
	}: {
		motd: string;
		class?: string;
	} = $props();

	let spans = $derived(parseMotd(motd));
</script>

<span class={cn('font-mono whitespace-pre-wrap', className)}>
	{#each spans as span, i (i)}
		<span
			style={span.color ? `color: ${span.color}` : ''}
			class:font-bold={span.bold}
			class:italic={span.italic}
			class:underline={span.underline}
			class:line-through={span.strike}
			class:opacity-70={span.obfuscated}>{span.text}</span
		>
	{/each}
</span>
