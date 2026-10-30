// Minecraft § formatting code parser for MOTD display

export interface MotdSpan {
	text: string;
	color: string | null;
	bold: boolean;
	italic: boolean;
	underline: boolean;
	strike: boolean;
	obfuscated: boolean;
}

const COLOR_CODES: Record<string, string> = {
	'0': '#000000',
	'1': '#0000AA',
	'2': '#00AA00',
	'3': '#00AAAA',
	'4': '#AA0000',
	'5': '#AA00AA',
	'6': '#FFAA00',
	'7': '#AAAAAA',
	'8': '#555555',
	'9': '#5555FF',
	a: '#55FF55',
	b: '#55FFFF',
	c: '#FF5555',
	d: '#FF55FF',
	e: '#FFFF55',
	f: '#FFFFFF'
};

const HEX_SEQUENCE = /^§x(§[0-9a-f]){6}$/i;
const FORMAT_CODES = new Set(['l', 'o', 'n', 'm', 'k', 'r']);

export function parseMotd(motd: string): MotdSpan[] {
	const spans: MotdSpan[] = [];
	let color: string | null = null;
	let bold = false;
	let italic = false;
	let underline = false;
	let strike = false;
	let obfuscated = false;
	let buffer = '';

	const flush = () => {
		if (buffer) {
			spans.push({ text: buffer, color, bold, italic, underline, strike, obfuscated });
			buffer = '';
		}
	};

	for (let i = 0; i < motd.length; i++) {
		const ch = motd[i];
		if (ch === '§' && i + 1 < motd.length) {
			const code = motd[i + 1].toLowerCase();

			// Modern hex colors arrive as §x followed by six codes
			if (code === 'x') {
				const seq = motd.slice(i, i + 14);
				if (HEX_SEQUENCE.test(seq)) {
					flush();
					color = '#' + seq.replace(/§/g, '').slice(1);
					bold = italic = underline = strike = obfuscated = false;
					i += 13;
					continue;
				}
			}

			if (COLOR_CODES[code]) {
				flush();
				color = COLOR_CODES[code];
				bold = italic = underline = strike = obfuscated = false;
				i++;
				continue;
			}

			if (FORMAT_CODES.has(code)) {
				flush();
				if (code === 'l') bold = true;
				else if (code === 'o') italic = true;
				else if (code === 'n') underline = true;
				else if (code === 'm') strike = true;
				else if (code === 'k') obfuscated = true;
				else if (code === 'r') {
					color = null;
					bold = italic = underline = strike = obfuscated = false;
				}
				i++;
				continue;
			}

			// Unknown code keeps the section sign literal
		}
		buffer += ch;
	}
	flush();
	return spans;
}
