<script lang="ts">
	import { untrack } from 'svelte';
	import * as monaco from 'monaco-editor';
	import editorWorker from 'monaco-editor/esm/vs/editor/editor.worker?worker';
	import jsonWorker from 'monaco-editor/esm/vs/language/json/json.worker?worker';
	import cssWorker from 'monaco-editor/esm/vs/language/css/css.worker?worker';
	import htmlWorker from 'monaco-editor/esm/vs/language/html/html.worker?worker';
	import tsWorker from 'monaco-editor/esm/vs/language/typescript/ts.worker?worker';

	const globalScope = self as typeof self & {
		MonacoEnvironment?: monaco.Environment;
		__jsonTemplateLang?: boolean;
	};

	// Configure the Monaco worker environment
	if (!globalScope.MonacoEnvironment) {
		globalScope.MonacoEnvironment = {
			getWorker(_: unknown, label: string) {
				if (label === 'json') return new jsonWorker();
				if (label === 'css' || label === 'scss' || label === 'less') return new cssWorker();
				if (label === 'html' || label === 'handlebars' || label === 'razor')
					return new htmlWorker();
				if (label === 'typescript' || label === 'javascript') return new tsWorker();
				return new editorWorker();
			}
		};
	}

	// MONACO PSEUDO JSON-LIKE TEMPLATE AND LINTER, NEEDED FOR GO TEMPLATING
	if (!globalScope.__jsonTemplateLang) {
		globalScope.__jsonTemplateLang = true;
		monaco.languages.register({ id: 'json-template' });
		monaco.languages.setLanguageConfiguration('json-template', {
			brackets: [
				['{', '}'],
				['[', ']']
			],
			autoClosingPairs: [
				{ open: '{', close: '}' },
				{ open: '[', close: ']' },
				{ open: '"', close: '"' }
			],
			surroundingPairs: [
				{ open: '{', close: '}' },
				{ open: '[', close: ']' },
				{ open: '"', close: '"' }
			]
		});
		monaco.languages.setMonarchTokensProvider('json-template', {
			defaultToken: '',
			tokenizer: {
				root: [
					[/\{\{/, { token: 'variable.template', next: '@template' }],
					[/"(?:[^"\\]|\\.)*"(?=\s*:)/, 'type'],
					[/"/, { token: 'string', next: '@string' }],
					[/-?\d+(?:\.\d+)?(?:[eE][+-]?\d+)?/, 'number'],
					[/\b(?:true|false|null)\b/, 'keyword'],
					[/[{}[\]]/, 'delimiter.bracket'],
					[/[,:]/, 'delimiter']
				],
				template: [
					[/\}\}/, { token: 'variable.template', next: '@pop' }],
					[/[^}]+/, 'variable.template'],
					[/\}/, 'variable.template']
				],
				string: [
					[/\{\{/, { token: 'variable.template', next: '@templateInString' }],
					[/[^"\\{]+/, 'string'],
					[/\\./, 'string.escape'],
					[/"/, { token: 'string', next: '@pop' }],
					[/\{/, 'string']
				],
				templateInString: [
					[/\}\}/, { token: 'variable.template', next: '@pop' }],
					[/[^}]+/, 'variable.template'],
					[/\}/, 'variable.template']
				]
			}
		} as monaco.languages.IMonarchLanguage);
	}

	interface Props {
		value?: string;
		language?: string;
		readOnly?: boolean;
		height?: string;
		onChange?: (value: string) => void;
	}

	let {
		value = '',
		language = 'json',
		readOnly = false,
		height = '300px',
		onChange
	}: Props = $props();

	let container = $state<HTMLDivElement>();
	let editor: monaco.editor.IStandaloneCodeEditor | null = null;
	let resizeObserver: ResizeObserver | null = null;

	// Guards onChange while pushing an external value into editor
	let applyingExternal = false;

	// Create the editor when the container mounts
	$effect(() => {
		if (!container) return;

		editor = monaco.editor.create(container, {
			value: untrack(() => value),
			language: untrack(() => language),
			readOnly: untrack(() => readOnly),
			theme: 'vs-dark',
			fontSize: 12,
			fontFamily: "'JetBrains Mono', 'Fira Code', 'Monaco', 'Consolas', monospace",
			minimap: { enabled: false },
			scrollBeyondLastLine: false,
			wordWrap: 'on',
			lineNumbers: 'on',
			automaticLayout: false,
			fixedOverflowWidgets: true,
			scrollbar: { useShadows: false }
		});

		editor.onDidChangeModelContent(() => {
			if (applyingExternal || !editor) return;
			onChange?.(editor.getValue());
		});

		resizeObserver = new ResizeObserver(() => editor?.layout());
		resizeObserver.observe(container);

		return () => {
			resizeObserver?.disconnect();
			resizeObserver = null;
			editor?.dispose();
			editor = null;
		};
	});

	// Push external value changes into the editor
	$effect(() => {
		const next = value;
		if (editor && editor.getValue() !== next) {
			applyingExternal = true;
			editor.setValue(next);
			applyingExternal = false;
		}
	});

	// Reflect read-only and language changes without recreating
	$effect(() => {
		editor?.updateOptions({ readOnly });
	});

	$effect(() => {
		const model = editor?.getModel();
		if (model) monaco.editor.setModelLanguage(model, language);
	});
</script>

<div
	bind:this={container}
	style="height: {height}; width: 100%;"
	class="overflow-hidden rounded-md border border-border/50"
></div>
