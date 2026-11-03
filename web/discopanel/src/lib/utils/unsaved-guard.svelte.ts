import { beforeNavigate } from '$app/navigation';

const DEFAULT_MESSAGE = 'You have unsaved changes. Leave without saving them?';

/**
 * Warns before unsaved edits are thrown away by in-app navigation (switching
 * server tabs, following a link) or by closing/reloading the page.
 *
 * Settings, Properties and Default properties all track dirty state and
 * show a save bar, but nothing stopped a tab switch from silently
 * discarding the edits. Call during component init.
 */
export function guardUnsavedChanges(isDirty: () => boolean, message = DEFAULT_MESSAGE): void {
	beforeNavigate((nav) => {
		if (!isDirty()) return;
		if (nav.type === 'leave') {
			// Page unload: cancelling makes the browser show its own prompt
			nav.cancel();
			return;
		}
		// Hash-only jumps inside a form (section anchors) keep the edits
		if (
			nav.from &&
			nav.to &&
			nav.from.url.pathname === nav.to.url.pathname &&
			nav.from.url.search === nav.to.url.search
		) {
			return;
		}
		if (!window.confirm(message)) nav.cancel();
	});
}
