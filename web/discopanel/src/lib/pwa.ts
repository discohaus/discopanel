import { browser } from '$app/environment';

export function isStandalonePwa(): boolean {
	if (!browser) return false;
	const iosNavigator = navigator as Navigator & { standalone?: boolean };
	return window.matchMedia('(display-mode: standalone)').matches || iosNavigator.standalone === true;
}