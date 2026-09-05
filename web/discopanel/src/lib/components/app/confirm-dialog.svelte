<script lang="ts">
	import {
		AlertDialog,
		AlertDialogContent,
		AlertDialogHeader,
		AlertDialogTitle,
		AlertDialogDescription,
		AlertDialogFooter,
		AlertDialogCancel
	} from '$lib/components/ui/alert-dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Loader2 } from '@lucide/svelte';
	import type { Snippet } from 'svelte';

	let {
		open = $bindable(false),
		title,
		description = '',
		confirmLabel = 'Confirm',
		destructive = false,
		requireText = '',
		onConfirm,
		children
	}: {
		open?: boolean;
		title: string;
		description?: string;
		confirmLabel?: string;
		destructive?: boolean;
		requireText?: string;
		onConfirm: () => void | Promise<void>;
		children?: Snippet;
	} = $props();

	let typed = $state('');
	let working = $state(false);

	let blocked = $derived((requireText !== '' && typed !== requireText) || working);

	$effect(() => {
		if (!open) typed = '';
	});

	async function confirm() {
		working = true;
		try {
			await onConfirm();
			open = false;
		} finally {
			working = false;
		}
	}
</script>

<AlertDialog bind:open>
	<AlertDialogContent>
		<AlertDialogHeader>
			<AlertDialogTitle>{title}</AlertDialogTitle>
			{#if description}
				<AlertDialogDescription class="whitespace-pre-line">{description}</AlertDialogDescription>
			{/if}
		</AlertDialogHeader>
		{#if children}
			{@render children()}
		{/if}
		{#if requireText}
			<div class="space-y-2">
				<Label for="confirm-text" class="text-sm text-muted-foreground">
					Type <span class="font-mono font-semibold text-foreground">{requireText}</span> to confirm
				</Label>
				<Input id="confirm-text" bind:value={typed} autocomplete="off" spellcheck={false} />
			</div>
		{/if}
		<AlertDialogFooter>
			<AlertDialogCancel disabled={working}>Cancel</AlertDialogCancel>
			<Button
				variant={destructive ? 'destructive' : 'default'}
				disabled={blocked}
				onclick={confirm}
			>
				{#if working}
					<Loader2 class="size-4 animate-spin" />
				{/if}
				{confirmLabel}
			</Button>
		</AlertDialogFooter>
	</AlertDialogContent>
</AlertDialog>
