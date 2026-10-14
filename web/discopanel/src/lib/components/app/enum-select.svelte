<script lang="ts">
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { enumLabelOr } from '$lib/proto-meta';
	import type { DescEnum } from '@bufbuild/protobuf';

	interface Props {
		schema: DescEnum;
		options: number[];
		value: number;
		class?: string;
		disabled?: boolean;
		name?: string;
	}

	let {
		schema,
		options,
		value = $bindable(),
		class: className = 'w-full',
		disabled = false,
		name
	}: Props = $props();
</script>

<Select
	type="single"
	value={String(value)}
	onValueChange={(v) => {
		if (v) value = Number(v);
	}}
	{name}
	{disabled}
>
	<SelectTrigger class={className}>
		<span class="truncate">{enumLabelOr(schema, value, options[0])}</span>
	</SelectTrigger>
	<SelectContent>
		{#each options as o (o)}
			<SelectItem value={String(o)}>{enumLabelOr(schema, o, o)}</SelectItem>
		{/each}
	</SelectContent>
</Select>
