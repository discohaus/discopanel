<script lang="ts">
	import { Input } from '$lib/components/ui/input';
	import { Label } from '$lib/components/ui/label';
	import { Checkbox } from '$lib/components/ui/checkbox';

	let {
		command = $bindable(''),
		delay = $bindable(0),
		restartAfterInit = $bindable(false)
	}: {
		command?: string;
		delay?: number;
		restartAfterInit?: boolean;
	} = $props();

	const uid = $props.id();
</script>

<div class="space-y-3 rounded-lg border bg-card p-4">
	<div class="space-y-1.5">
		<Label for="{uid}-command">Command</Label>
		<Input
			id="{uid}-command"
			bind:value={command}
			placeholder="sh -c 'sed -i ...'"
			class="font-mono"
		/>
		<p class="text-xs text-muted-foreground">
			Shell command to exec inside the container after start
		</p>
	</div>
	<div class="grid gap-4 sm:grid-cols-2">
		<div class="space-y-1.5">
			<Label for="{uid}-delay">Delay (seconds)</Label>
			<Input id="{uid}-delay" type="number" bind:value={delay} min={0} />
			<p class="text-xs text-muted-foreground">Seconds to wait after start before running</p>
		</div>
		<label class="flex cursor-pointer items-center gap-2 sm:pt-6">
			<Checkbox bind:checked={restartAfterInit} />
			<div>
				<span class="text-sm font-medium">Restart after init</span>
				<p class="text-xs text-muted-foreground">Restart the container after the command runs</p>
			</div>
		</label>
	</div>
</div>
