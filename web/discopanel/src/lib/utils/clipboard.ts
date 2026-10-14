// Copies text with a fallback for insecure contexts
export async function copyToClipboard(text: string): Promise<boolean> {
	if (navigator.clipboard?.writeText) {
		try {
			await navigator.clipboard.writeText(text);
			return true;
		} catch {
			try {
				const textArea = document.createElement('textarea');
				textArea.value = text;
				textArea.style.position = 'fixed';
				textArea.style.left = '-9999px';
				textArea.style.top = '-9999px';
				document.body.appendChild(textArea);
				textArea.focus();
				textArea.select();
				const success = document.execCommand('copy');
				document.body.removeChild(textArea);
				return success;
			} catch {
				return false;
			}
		}
	}

	return false;
}
