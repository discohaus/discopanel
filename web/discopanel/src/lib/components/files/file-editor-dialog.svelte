<script lang="ts">
	import {
		Dialog,
		DialogContent,
		DialogDescription,
		DialogFooter,
		DialogHeader,
		DialogTitle
	} from '$lib/components/ui/dialog';
	import { Button } from '$lib/components/ui/button';
	import { ConfirmDialog } from '$lib/components/app';
	import { rpcClient } from '$lib/api/rpc-client';
	import { notify } from '$lib/stores/activity.svelte';
	import { Loader2, Save, X, Maximize2, Minimize2 } from '@lucide/svelte';
	import type { FileInfo } from '$lib/proto/discopanel/v1/file_pb';
	import * as monaco from 'monaco-editor';
	import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
	import jsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker';
	import cssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker';
	import htmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker';
	import tsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker';

	// Registers global monaco workers once
	if (!self.MonacoEnvironment) {
		self.MonacoEnvironment = {
			getWorker(_, label: string) {
				if (label === 'json') {
					return new jsonWorker();
				}
				if (label === 'css' || label === 'scss' || label === 'less') {
					return new cssWorker();
				}
				if (label === 'html' || label === 'handlebars' || label === 'razor') {
					return new htmlWorker();
				}
				if (label === 'typescript' || label === 'javascript') {
					return new tsWorker();
				}
				return new editorWorker();
			}
		};
	}

	interface Props {
		serverId: string;
		file: FileInfo | null;
		open: boolean;
		onClose: () => void;
		onSave?: () => void;
	}

	let { serverId, file, open = false, onClose, onSave }: Props = $props();

	let content = $state('');
	let originalContent = $state('');
	let loading = $state(false);
	let saving = $state(false);
	let isDirty = $derived(content !== originalContent);
	let isFullscreen = $state(false);
	let isLargeFile = $state(false);
	let showDiscardConfirm = $state(false);
	let editorContainer = $state<HTMLDivElement>();
	let editor = $state<monaco.editor.IStandaloneCodeEditor | null>(null);
	let loadedFilePath = $state<string | null>(null);
	let resizeObserver = $state<ResizeObserver | null>(null);
	let contentLoaded = $state(false);

	// Loads file content when dialog opens
	$effect(() => {
		if (open && file && !file.isDir && file.path !== loadedFilePath) {
			loadedFilePath = file.path;
			loadFileContent();
		}
	});

	// Resets state when dialog closes
	$effect(() => {
		if (!open) {
			content = '';
			originalContent = '';
			isFullscreen = false;
			isLargeFile = false;
			showDiscardConfirm = false;
			loadedFilePath = null;
			contentLoaded = false;
			if (editor) {
				editor.dispose();
				editor = null;
			}
			if (resizeObserver) {
				resizeObserver.disconnect();
				resizeObserver = null;
			}
		}
	});

	// Creates editor once content arrives
	$effect(() => {
		if (open && editorContainer && contentLoaded && !editor && !loading) {
			createEditor();
		}
	});

	// Disposes editor on unmount
	$effect(() => {
		return () => {
			if (editor) {
				editor.dispose();
			}
			if (resizeObserver) {
				resizeObserver.disconnect();
			}
		};
	});

	async function loadFileContent() {
		if (!file) return;

		loading = true;
		try {
			const response = await rpcClient.file.getFile({ serverId: serverId, path: file.path });
			const text = new TextDecoder().decode(response.content);
			content = text;
			originalContent = text;
			contentLoaded = true;
		} catch {
			notify.error('Failed to load file content');
			onClose();
		} finally {
			loading = false;
		}
	}

	async function handleSave() {
		if (!file || !isDirty) return;

		saving = true;
		try {
			await rpcClient.file.updateFile({
				serverId: serverId,
				path: file.path,
				content: new TextEncoder().encode(content)
			});
			notify.success('File saved successfully');
			originalContent = content;
			onSave?.();
		} catch {
			notify.error('Failed to save file');
		} finally {
			saving = false;
		}
	}

	// Prompts for dirty buffers before closing
	function requestClose() {
		if (isDirty) {
			showDiscardConfirm = true;
			return;
		}
		onClose();
	}

	function createEditor() {
		if (!editorContainer) return;

		// Files over 100KB get reduced features
		isLargeFile = content.length > 100000;

		editor = monaco.editor.create(editorContainer, {
			value: content,
			language: file ? getFileLanguage(file.name) : 'plaintext',
			theme: 'vs-dark',
			fontSize: 14,
			fontFamily: "'JetBrains Mono Variable', 'JetBrains Mono', 'Monaco', 'Consolas', monospace",
			minimap: { enabled: !isFullscreen && !isLargeFile },
			scrollBeyondLastLine: false,
			wordWrap: isLargeFile ? 'off' : 'on',
			lineNumbers: 'on',
			renderWhitespace: 'selection',
			bracketPairColorization: { enabled: !isLargeFile },
			formatOnPaste: false,
			formatOnType: false,
			automaticLayout: false,
			fixedOverflowWidgets: true,
			suggest: {
				showWords: !isLargeFile,
				showSnippets: !isLargeFile
			},
			folding: !isLargeFile,
			renderLineHighlight: 'line',
			scrollbar: {
				useShadows: false
			},
			mouseWheelZoom: false,
			quickSuggestions: !isLargeFile
		});

		// Relayouts manually for performance
		resizeObserver = new ResizeObserver(() => {
			editor?.layout();
		});
		resizeObserver.observe(editorContainer);

		// Binds save shortcut
		editor.addCommand(monaco.KeyMod.CtrlCmd | monaco.KeyCode.KeyS, () => {
			handleSave();
		});

		// Syncs buffer into state
		editor.onDidChangeModelContent(() => {
			if (editor) {
				content = editor.getValue();
			}
		});

		editor.focus();
	}

	function getFileLanguage(fileName: string): string {
		const ext = fileName.toLowerCase().split('.').pop() || '';
		const languageMap: Record<string, string> = {
			js: 'javascript',
			ts: 'typescript',
			jsx: 'javascript',
			tsx: 'typescript',
			json: 'json',
			yml: 'yaml',
			yaml: 'yaml',
			toml: 'toml',
			properties: 'properties',
			conf: 'conf',
			cfg: 'ini',
			ini: 'ini',
			xml: 'xml',
			html: 'html',
			css: 'css',
			scss: 'scss',
			sass: 'sass',
			less: 'less',
			md: 'markdown',
			py: 'python',
			java: 'java',
			cpp: 'cpp',
			c: 'c',
			h: 'c',
			cs: 'csharp',
			go: 'go',
			rs: 'rust',
			php: 'php',
			rb: 'ruby',
			lua: 'lua',
			sh: 'bash',
			bash: 'bash',
			zsh: 'bash',
			fish: 'bash',
			ps1: 'powershell',
			bat: 'batch',
			cmd: 'batch',
			dockerfile: 'dockerfile',
			makefile: 'makefile',
			gradle: 'groovy',
			groovy: 'groovy',
			kt: 'kotlin',
			swift: 'swift',
			r: 'r',
			scala: 'scala',
			sql: 'sql',
			pl: 'perl',
			vim: 'vim'
		};
		return languageMap[ext] || 'plaintext';
	}

	function toggleFullscreen() {
		isFullscreen = !isFullscreen;
		if (editor) {
			editor.updateOptions({
				minimap: { enabled: !isFullscreen && !isLargeFile }
			});
		}
	}
</script>

<Dialog {open} onOpenChange={(isOpen) => !isOpen && requestClose()}>
	<DialogContent
		showCloseButton={false}
		onEscapeKeydown={(e) => {
			if (isDirty) {
				e.preventDefault();
				showDiscardConfirm = true;
			}
		}}
		onInteractOutside={(e) => {
			if (isDirty) {
				e.preventDefault();
				showDiscardConfirm = true;
			}
		}}
		class={isFullscreen
			? 'flex h-[95vh] w-[95vw]! max-w-[95vw]! flex-col sm:max-w-[95vw]!'
			: 'flex h-[85vh] w-[90vw]! max-w-[90vw]! flex-col sm:max-w-[90vw]!'}
	>
		<div class="absolute top-4 right-4 flex gap-1">
			<Button
				variant="ghost"
				size="icon"
				class="size-8"
				onclick={toggleFullscreen}
				title={isFullscreen ? 'Exit fullscreen' : 'Enter fullscreen'}
			>
				{#if isFullscreen}
					<Minimize2 class="size-4" />
				{:else}
					<Maximize2 class="size-4" />
				{/if}
			</Button>
			<Button variant="ghost" size="icon" class="size-8" onclick={requestClose} title="Close">
				<X class="size-4" />
				<span class="sr-only">Close</span>
			</Button>
		</div>
		<DialogHeader class="shrink-0">
			<DialogTitle class="flex items-center gap-2 font-mono text-base">
				{#if file}
					{file.name}
					{#if isDirty}
						<span class="text-sm text-status-warn">*</span>
					{/if}
				{:else}
					File editor
				{/if}
			</DialogTitle>
			<DialogDescription class="font-mono text-xs">
				{#if file}
					{file.path}
				{/if}
			</DialogDescription>
		</DialogHeader>

		<div class="relative min-h-0 flex-1 overflow-hidden rounded-md border bg-terminal">
			{#if loading}
				<div class="absolute inset-0 z-10 flex items-center justify-center bg-background/80">
					<Loader2 class="size-8 animate-spin text-muted-foreground" />
				</div>
			{/if}
			<div bind:this={editorContainer} class="h-full w-full"></div>
		</div>

		<DialogFooter class="shrink-0">
			<div class="flex w-full items-center justify-between gap-3">
				<div class="flex min-w-0 items-center gap-4 text-xs text-muted-foreground">
					<span class="stat-label">
						{#if file}
							{getFileLanguage(file.name)}
						{:else}
							plain text
						{/if}
					</span>
					<span class="tabular hidden sm:inline">
						{content.split('\n').length} lines, {content.length} characters
					</span>
					{#if isDirty}
						<span class="text-status-warn">Modified</span>
					{:else}
						<span class="text-status-ok">Saved</span>
					{/if}
				</div>
				<div class="flex shrink-0 items-center gap-2">
					<span class="hidden text-xs text-muted-foreground sm:inline">Ctrl+S to save</span>
					<Button variant="outline" onclick={requestClose}>
						<X class="size-4" />
						Close
					</Button>
					<Button
						onclick={handleSave}
						disabled={!isDirty || saving || loading}
						variant={isDirty ? 'default' : 'secondary'}
					>
						{#if saving}
							<Loader2 class="size-4 animate-spin" />
						{:else}
							<Save class="size-4" />
						{/if}
						Save
					</Button>
				</div>
			</div>
		</DialogFooter>
	</DialogContent>
</Dialog>

<ConfirmDialog
	bind:open={showDiscardConfirm}
	title="Discard unsaved changes?"
	description={`Your edits to ${file?.name ?? 'this file'} have not been saved and will be lost.`}
	confirmLabel="Discard changes"
	destructive
	onConfirm={() => onClose()}
/>
