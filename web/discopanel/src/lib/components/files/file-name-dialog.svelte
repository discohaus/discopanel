<script lang="ts">
	import { Dialog as DialogPrimitive } from 'bits-ui';
	import {
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';

	interface Props {
		open?: boolean;
		id: string;
		title: string;
		description: string;
		subject: string;
		label: string;
		placeholder?: string;
		confirmLabel: string;
		value?: string;
		onConfirm: () => void;
	}

	let {
		open = $bindable(false),
		id,
		title,
		description,
		subject,
		label,
		placeholder = '',
		confirmLabel,
		value = $bindable(''),
		onConfirm
	}: Props = $props();
</script>

<DialogPrimitive.Root bind:open>
	<DialogContent>
		<DialogHeader>
			<DialogTitle>{title}</DialogTitle>
			<DialogDescription>
				{description} <span class="font-mono">{subject}</span>
			</DialogDescription>
		</DialogHeader>
		<div class="grid gap-4 py-4">
			<div class="grid gap-2">
				<Label for={id}>{label}</Label>
				<Input
					{id}
					bind:value
					{placeholder}
					class="font-mono"
					onkeydown={(e) => {
						if (e.key === 'Enter') onConfirm();
					}}
				/>
			</div>
		</div>
		<DialogFooter>
			<Button variant="outline" onclick={() => (open = false)}>Cancel</Button>
			<Button onclick={onConfirm}>{confirmLabel}</Button>
		</DialogFooter>
	</DialogContent>
</DialogPrimitive.Root>
