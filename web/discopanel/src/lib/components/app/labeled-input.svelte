<script lang="ts">
	import type { HTMLInputAttributes, HTMLInputTypeAttribute } from 'svelte/elements';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';

	type Props = Omit<HTMLInputAttributes, 'type' | 'files'> & {
		id: string;
		label: string;
		hint?: string;
		type?: Exclude<HTMLInputTypeAttribute, 'file'>;
	};

	let { id, label, hint = '', type, value = $bindable(), ...rest }: Props = $props();
</script>

<div class="space-y-2">
	<Label for={id}>{label}</Label>
	<Input {id} {type} bind:value {...rest} />
	{#if hint}
		<p class="text-xs text-muted-foreground">{hint}</p>
	{/if}
</div>
