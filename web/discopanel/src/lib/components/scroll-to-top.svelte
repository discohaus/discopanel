<script lang="ts">
	import { onMount } from 'svelte';
	import { Button } from '$lib/components/ui/button';
	import { ArrowUp } from '@lucide/svelte';

	let showButton = $state(false);
	let scrollElement = $state<Element | Window | null>(null);

	onMount(() => {
		// Function to find the scrollable element
		const findScrollableElement = () => {
			// Pages with a pinned header mark their own scroll pane
			const marked = document.querySelector('[data-scroll-root]');
			if (marked) {
				return marked;
			}

			// Check the main element
			const mainElement = document.querySelector('main');
			if (mainElement) {
				// Check if main scrolls
				const mainStyle = window.getComputedStyle(mainElement);
				if (mainStyle.overflowY === 'scroll' || mainStyle.overflowY === 'auto') {
					if (mainElement.scrollHeight > mainElement.clientHeight) {
						return mainElement;
					}
				}

				// Check main's parent (SidebarInset)
				const parent = mainElement.parentElement;
				if (parent) {
					const parentStyle = window.getComputedStyle(parent);
					if (parentStyle.overflowY === 'scroll' || parentStyle.overflowY === 'auto') {
						if (parent.scrollHeight > parent.clientHeight) {
							return parent;
						}
					}
				}
			}

			// Default to window
			return window;
		};

		scrollElement = findScrollableElement();

		const handleScroll = () => {
			if (scrollElement === window) {
				showButton = window.scrollY > 200;
			} else if (scrollElement) {
				showButton = (scrollElement as Element).scrollTop > 200;
			}
		};

		// Add scroll listener
		const target = scrollElement === window ? window : (scrollElement as Element);
		target.addEventListener('scroll', handleScroll);

		// Check initial scroll position
		handleScroll();

		return () => {
			target.removeEventListener('scroll', handleScroll);
		};
	});

	function scrollToTop() {
		if (scrollElement === window) {
			window.scrollTo({ top: 0, behavior: 'smooth' });
		} else if (scrollElement) {
			(scrollElement as Element).scrollTo({ top: 0, behavior: 'smooth' });
		}
	}
</script>

{#if showButton}
	<div class="fixed right-8 bottom-8 z-50">
		<Button
			size="icon"
			onclick={scrollToTop}
			class="bg-primary text-primary-foreground shadow-lg hover:bg-primary/90"
		>
			<ArrowUp class="size-5" />
		</Button>
	</div>
{/if}
