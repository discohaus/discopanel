<script lang="ts">
	import { Input } from '$lib/components/ui/input';
	import { Button } from '$lib/components/ui/button';
	import { Switch } from '$lib/components/ui/switch';
	import { Select, SelectContent, SelectItem, SelectTrigger } from '$lib/components/ui/select';
	import { Link, CircleDot, Circle } from '@lucide/svelte';
	import type { ServerProperty } from '$lib/proto/discopanel/v1/properties_pb';
	import type { PropertiesForm } from './properties-form.svelte';
	import SettingRow from '$lib/components/app/setting-row.svelte';

	let {
		form,
		prop,
		locked = false,
		highlighted = false,
		onCopyLink
	}: {
		form: PropertiesForm;
		prop: ServerProperty;
		locked?: boolean;
		highlighted?: boolean;
		onCopyLink?: (key: string) => void;
	} = $props();

	let isEnabled = $derived(form.enabled.has(prop.key));
	let isModified = $derived(form.modifiedKeys.has(prop.key));
	let canToggle = $derived(!locked && !prop.required && !prop.system);
	let inputDisabled = $derived(locked || prop.system || !isEnabled);

	let hint = $derived(
		prop.defaultValue !== undefined && prop.defaultValue !== ''
			? `Default: ${prop.defaultValue}`
			: ''
	);
</script>

<SettingRow
	id={prop.key}
	label={prop.label}
	description={prop.description}
	envVar={prop.envVar}
	{hint}
	required={prop.required}
	system={prop.system}
	modified={isModified}
	unset={!isEnabled}
	dimmed={!isEnabled}
	{highlighted}
>
	{#if prop.type === 'checkbox'}
		<div class="flex h-9 items-center gap-3">
			<Switch
				id={prop.key}
				checked={form.boolValue(prop)}
				onCheckedChange={(checked) => form.setValue(prop.key, checked)}
				disabled={inputDisabled}
			/>
			<span class="text-sm {!isEnabled ? 'text-muted-foreground' : ''}">
				{form.boolValue(prop) ? 'Enabled' : 'Disabled'}
			</span>
		</div>
	{:else if prop.type === 'select' && prop.options?.length}
		<Select
			type="single"
			value={form.displayValue(prop)}
			onValueChange={(value) => form.setValue(prop.key, value ?? '')}
			disabled={inputDisabled}
		>
			<SelectTrigger class="h-9 w-full {!isEnabled ? 'opacity-60' : ''}">
				<span class="truncate">
					{form.displayValue(prop) || 'Select...'}
				</span>
			</SelectTrigger>
			<SelectContent>
				{#each prop.options as option (option)}
					<SelectItem value={option}>{option || '(empty)'}</SelectItem>
				{/each}
			</SelectContent>
		</Select>
	{:else}
		<Input
			id={prop.key}
			type={prop.type === 'number' ? 'number' : prop.type === 'password' ? 'password' : 'text'}
			value={form.displayValue(prop)}
			placeholder={prop.defaultValue ?? ''}
			oninput={(e) => form.setValue(prop.key, e.currentTarget.value)}
			disabled={inputDisabled}
			class="h-9 {!isEnabled ? 'opacity-60' : ''}"
		/>
	{/if}

	{#snippet actions()}
		<button
			class="rounded p-1 transition-colors
				{canToggle ? 'cursor-pointer hover:bg-muted' : 'cursor-not-allowed opacity-40'}"
			onclick={() => canToggle && form.toggle(prop.key, !isEnabled, prop)}
			disabled={!canToggle}
			title={isEnabled ? 'Click to unset (use default)' : 'Click to set a custom value'}
		>
			{#if isEnabled}
				<CircleDot class="size-4 text-status-ok" />
			{:else}
				<Circle class="size-4 text-muted-foreground" />
			{/if}
		</button>
		{#if onCopyLink}
			<Button
				variant="ghost"
				size="icon"
				class="size-6 opacity-0 group-hover:opacity-100 focus-visible:opacity-100"
				onclick={() => onCopyLink?.(prop.key)}
				title="Copy link to this setting"
			>
				<Link class="size-3" />
			</Button>
		{/if}
	{/snippet}
</SettingRow>
