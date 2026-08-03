import type { Certificate } from '$lib/proto/discopanel/v1/storage_pb';

// One decoded pem block
export interface PemBlock {
	type: string;
	pem: string;
	der: Uint8Array;
}

// What the upload parser understood
export interface ParsedUpload {
	certPem: string;
	keyPem: string;
	names: string[];
	notAfter: Date | null;
	issuer: string;
	keyStatus: 'match' | 'mismatch' | 'unknown' | 'missing';
	error: string;
}

const PEM_RE = /-----BEGIN ([A-Z0-9 ]+)-----([\s\S]*?)-----END \1-----/g;

const KEY_TYPES = new Set(['PRIVATE KEY', 'RSA PRIVATE KEY', 'EC PRIVATE KEY']);

// Splits pasted text into decoded pem blocks
export function parsePemBlocks(text: string): PemBlock[] {
	const blocks: PemBlock[] = [];
	for (const match of text.matchAll(PEM_RE)) {
		const type = match[1];
		const body = match[2].replace(/[^A-Za-z0-9+/=]/g, '');
		try {
			const raw = atob(body);
			const der = new Uint8Array(raw.length);
			for (let i = 0; i < raw.length; i++) der[i] = raw.charCodeAt(i);
			blocks.push({ type, pem: match[0], der });
		} catch {
			// Broken base64 blocks just drop out
		}
	}
	return blocks;
}

// Minimal der tlv reader
interface Tlv {
	tag: number;
	value: Uint8Array;
	raw: Uint8Array;
	end: number;
}

function readTlv(buf: Uint8Array, offset: number): Tlv | null {
	if (offset + 2 > buf.length) return null;
	const tag = buf[offset];
	let len = buf[offset + 1];
	let head = 2;
	if (len & 0x80) {
		const n = len & 0x7f;
		if (n === 0 || n > 4 || offset + 2 + n > buf.length) return null;
		len = 0;
		for (let i = 0; i < n; i++) len = len * 256 + buf[offset + 2 + i];
		head = 2 + n;
	}
	const start = offset + head;
	const end = start + len;
	if (end > buf.length) return null;
	return { tag, value: buf.subarray(start, end), raw: buf.subarray(offset, end), end };
}

// Children of one constructed tlv
function readChildren(value: Uint8Array): Tlv[] {
	const out: Tlv[] = [];
	let offset = 0;
	while (offset < value.length) {
		const tlv = readTlv(value, offset);
		if (!tlv) break;
		out.push(tlv);
		offset = tlv.end;
	}
	return out;
}

function bytesEqual(a: Uint8Array, b: Uint8Array): boolean {
	if (a.length !== b.length) return false;
	for (let i = 0; i < a.length; i++) if (a[i] !== b[i]) return false;
	return true;
}

// Der oid bytes for the fields we walk to
const OID_CN = new Uint8Array([0x55, 0x04, 0x03]);
const OID_SAN = new Uint8Array([0x55, 0x1d, 0x11]);

function decodeTime(tlv: Tlv): Date | null {
	const text = new TextDecoder().decode(tlv.value);
	// Utc times carry two digit years
	if (tlv.tag === 0x17) {
		const m = text.match(/^(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})?Z$/);
		if (!m) return null;
		const year = Number(m[1]) >= 50 ? 1900 + Number(m[1]) : 2000 + Number(m[1]);
		return new Date(
			Date.UTC(
				year,
				Number(m[2]) - 1,
				Number(m[3]),
				Number(m[4]),
				Number(m[5]),
				Number(m[6] ?? '0')
			)
		);
	}
	const m = text.match(/^(\d{4})(\d{2})(\d{2})(\d{2})(\d{2})(\d{2})?/);
	if (!m) return null;
	return new Date(
		Date.UTC(
			Number(m[1]),
			Number(m[2]) - 1,
			Number(m[3]),
			Number(m[4]),
			Number(m[5]),
			Number(m[6] ?? '0')
		)
	);
}

// Common name text out of an x501 name
function nameCN(name: Tlv): string {
	for (const rdn of readChildren(name.value)) {
		for (const atv of readChildren(rdn.value)) {
			const parts = readChildren(atv.value);
			if (parts.length === 2 && parts[0].tag === 0x06 && bytesEqual(parts[0].value, OID_CN)) {
				return new TextDecoder().decode(parts[1].value);
			}
		}
	}
	return '';
}

// Facts a certificate der carries
export interface CertFacts {
	names: string[];
	notBefore: Date | null;
	notAfter: Date | null;
	issuer: string;
	subjectDer: Uint8Array;
	issuerDer: Uint8Array;
	spkiDer: Uint8Array;
}

// Walks one certificate der for display facts
export function analyzeCertificate(der: Uint8Array): CertFacts | null {
	const cert = readTlv(der, 0);
	if (!cert || cert.tag !== 0x30) return null;
	const tbs = readChildren(cert.value)[0];
	if (!tbs || tbs.tag !== 0x30) return null;
	const fields = readChildren(tbs.value);
	let i = 0;
	// Version rides an explicit zero tag
	if (fields[i]?.tag === 0xa0) i++;
	i++; // serial
	i++; // signature algorithm
	const issuer = fields[i++];
	const validity = fields[i++];
	const subject = fields[i++];
	const spki = fields[i++];
	if (!issuer || !validity || !subject || !spki) return null;

	const times = readChildren(validity.value);
	const facts: CertFacts = {
		names: [],
		notBefore: times[0] ? decodeTime(times[0]) : null,
		notAfter: times[1] ? decodeTime(times[1]) : null,
		issuer: nameCN(issuer),
		subjectDer: subject.raw,
		issuerDer: issuer.raw,
		spkiDer: spki.raw
	};

	// San extension lives behind context tag three
	for (const field of fields.slice(i)) {
		if (field.tag !== 0xa3) continue;
		const extensions = readChildren(field.value)[0];
		if (!extensions) continue;
		for (const ext of readChildren(extensions.value)) {
			const parts = readChildren(ext.value);
			if (!parts[0] || parts[0].tag !== 0x06 || !bytesEqual(parts[0].value, OID_SAN)) continue;
			const octet = parts[parts.length - 1];
			if (!octet || octet.tag !== 0x04) continue;
			const generalNames = readTlv(octet.value, 0);
			if (!generalNames) continue;
			for (const gn of readChildren(generalNames.value)) {
				// Dns names ride context tag two
				if (gn.tag === 0x82) {
					facts.names.push(new TextDecoder().decode(gn.value).toLowerCase());
				}
			}
		}
	}
	if (facts.names.length === 0) {
		const cn = nameCN(subject);
		if (cn) facts.names.push(cn.toLowerCase());
	}
	facts.names.sort();
	return facts;
}

function b64urlToBytes(b64url: string): Uint8Array {
	const b64 = b64url.replace(/-/g, '+').replace(/_/g, '/');
	const raw = atob(b64 + '='.repeat((4 - (b64.length % 4)) % 4));
	const out = new Uint8Array(raw.length);
	for (let i = 0; i < raw.length; i++) out[i] = raw.charCodeAt(i);
	return out;
}

// Strips leading zero padding for integer compares
function trimZeros(bytes: Uint8Array): Uint8Array {
	let start = 0;
	while (start < bytes.length - 1 && bytes[start] === 0) start++;
	return bytes.subarray(start);
}

const EC_CURVES: Record<string, { name: string; size: number }> = {
	'1.2.840.10045.3.1.7': { name: 'P-256', size: 32 },
	'1.3.132.0.34': { name: 'P-384', size: 48 },
	'1.3.132.0.35': { name: 'P-521', size: 66 }
};

function decodeOid(bytes: Uint8Array): string {
	if (bytes.length === 0) return '';
	const parts = [Math.floor(bytes[0] / 40), bytes[0] % 40];
	let acc = 0;
	for (let i = 1; i < bytes.length; i++) {
		acc = acc * 128 + (bytes[i] & 0x7f);
		if (!(bytes[i] & 0x80)) {
			parts.push(acc);
			acc = 0;
		}
	}
	return parts.join('.');
}

// Builds a pkcs8 wrapper around a legacy key body
function wrapPkcs8(algorithmDer: number[], keyDer: Uint8Array): Uint8Array {
	const inner = [0x04, ...encodeLen(keyDer.length), ...keyDer];
	const body = [0x02, 0x01, 0x00, ...algorithmDer, ...inner];
	return new Uint8Array([0x30, ...encodeLen(body.length), ...body]);
}

function encodeLen(len: number): number[] {
	if (len < 0x80) return [len];
	const bytes: number[] = [];
	let rest = len;
	while (rest > 0) {
		bytes.unshift(rest & 0xff);
		rest >>= 8;
	}
	return [0x80 | bytes.length, ...bytes];
}

const RSA_ALG_DER = [
	0x30, 0x0d, 0x06, 0x09, 0x2a, 0x86, 0x48, 0x86, 0xf7, 0x0d, 0x01, 0x01, 0x01, 0x05, 0x00
];

// Curve oid pulled out of a sec1 key body
function sec1Curve(der: Uint8Array): string {
	const seq = readTlv(der, 0);
	if (!seq) return '';
	for (const child of readChildren(seq.value)) {
		if (child.tag === 0xa0) {
			const oid = readTlv(child.value, 0);
			if (oid && oid.tag === 0x06) return decodeOid(oid.value);
		}
	}
	return '';
}

// Ec algorithm identifier der for one curve oid
function ecAlgDer(curveOid: string): number[] | null {
	const oidBytes: Record<string, number[]> = {
		'1.2.840.10045.3.1.7': [0x06, 0x08, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x03, 0x01, 0x07],
		'1.3.132.0.34': [0x06, 0x05, 0x2b, 0x81, 0x04, 0x00, 0x22],
		'1.3.132.0.35': [0x06, 0x05, 0x2b, 0x81, 0x04, 0x00, 0x23]
	};
	const curve = oidBytes[curveOid];
	if (!curve) return null;
	const body = [0x06, 0x07, 0x2a, 0x86, 0x48, 0xce, 0x3d, 0x02, 0x01, ...curve];
	return [0x30, ...encodeLen(body.length), ...body];
}

// Public numbers parsed straight from the cert spki
function spkiPublic(
	spkiDer: Uint8Array
): { kind: 'rsa'; n: Uint8Array } | { kind: 'ec'; point: Uint8Array } | null {
	const spki = readTlv(spkiDer, 0);
	if (!spki) return null;
	const [alg, bits] = readChildren(spki.value);
	if (!alg || !bits || bits.tag !== 0x03) return null;
	const algOidTlv = readChildren(alg.value)[0];
	if (!algOidTlv) return null;
	const oid = decodeOid(algOidTlv.value);
	const key = bits.value.subarray(1);
	if (oid === '1.2.840.113549.1.1.1') {
		const rsaSeq = readTlv(key, 0);
		if (!rsaSeq) return null;
		const [n] = readChildren(rsaSeq.value);
		if (!n || n.tag !== 0x02) return null;
		return { kind: 'rsa', n: trimZeros(n.value) };
	}
	if (oid === '1.2.840.10045.2.1') {
		return { kind: 'ec', point: key };
	}
	return null;
}

// Compares a private key against the leaf public key
export async function keyMatchesCert(
	keyBlock: PemBlock,
	certFacts: CertFacts
): Promise<'match' | 'mismatch' | 'unknown'> {
	const pub = spkiPublic(certFacts.spkiDer);
	if (!pub) return 'unknown';

	let pkcs8 = keyBlock.der;
	let curveHint = '';
	if (keyBlock.type === 'RSA PRIVATE KEY') {
		pkcs8 = wrapPkcs8(RSA_ALG_DER, keyBlock.der);
	} else if (keyBlock.type === 'EC PRIVATE KEY') {
		curveHint = sec1Curve(keyBlock.der);
		const alg = ecAlgDer(curveHint);
		if (!alg) return 'unknown';
		pkcs8 = wrapPkcs8(alg, keyBlock.der);
	}

	const attempts: Array<RsaHashedImportParams | EcKeyImportParams> = [];
	if (pub.kind === 'rsa') {
		attempts.push({ name: 'RSASSA-PKCS1-v1_5', hash: 'SHA-256' });
	} else {
		for (const curve of curveHint
			? [EC_CURVES[curveHint]?.name].filter(Boolean)
			: ['P-256', 'P-384', 'P-521']) {
			attempts.push({ name: 'ECDSA', namedCurve: curve as string });
		}
	}

	for (const alg of attempts) {
		try {
			const key = await crypto.subtle.importKey('pkcs8', pkcs8.slice().buffer, alg, true, ['sign']);
			const jwk = await crypto.subtle.exportKey('jwk', key);
			if (pub.kind === 'rsa' && jwk.n) {
				return bytesEqual(trimZeros(b64urlToBytes(jwk.n)), pub.n) ? 'match' : 'mismatch';
			}
			if (pub.kind === 'ec' && jwk.x && jwk.y) {
				const x = b64urlToBytes(jwk.x);
				const y = b64urlToBytes(jwk.y);
				const point = new Uint8Array(1 + x.length + y.length);
				point[0] = 4;
				point.set(x, 1);
				point.set(y, 1 + x.length);
				return bytesEqual(point, pub.point) ? 'match' : 'mismatch';
			}
		} catch {
			continue;
		}
	}
	return 'unknown';
}

// Classifies pasted text or files into one upload
export async function parseUpload(text: string): Promise<ParsedUpload> {
	const out: ParsedUpload = {
		certPem: '',
		keyPem: '',
		names: [],
		notAfter: null,
		issuer: '',
		keyStatus: 'missing',
		error: ''
	};
	if (/ENCRYPTED PRIVATE KEY|Proc-Type: 4,ENCRYPTED/.test(text)) {
		out.error = 'The key is password protected, export it without a password first';
		return out;
	}
	const blocks = parsePemBlocks(text);
	const certs = blocks.filter((b) => b.type === 'CERTIFICATE');
	const keys = blocks.filter((b) => KEY_TYPES.has(b.type));
	if (certs.length === 0 && keys.length === 0) {
		out.error = text.trim() ? 'No certificate or key found in that text' : '';
		return out;
	}
	if (keys.length > 1) {
		out.error = 'More than one private key found, add exactly one';
		return out;
	}
	// Missing parts stay pending, not err
	if (certs.length === 0) {
		out.keyPem = keys[0].pem;
		out.keyStatus = 'unknown';
		return out;
	}

	// Leaf never signs another cert in the bundle
	const facts = certs.map((c) => analyzeCertificate(c.der));
	if (facts.some((f) => f === null)) {
		out.error = 'One certificate block would not parse';
		return out;
	}
	const parsed = facts as CertFacts[];
	let leafIdx = 0;
	for (let i = 0; i < parsed.length; i++) {
		const isIssuer = parsed.some(
			(other, j) => j !== i && bytesEqual(other.issuerDer, parsed[i].subjectDer)
		);
		if (!isIssuer) {
			leafIdx = i;
			break;
		}
	}
	const leaf = parsed[leafIdx];
	const ordered = [certs[leafIdx], ...certs.filter((_, j) => j !== leafIdx)];

	out.certPem = ordered.map((c) => c.pem).join('\n');
	out.names = leaf.names;
	out.notAfter = leaf.notAfter;
	out.issuer = leaf.issuer;
	if (leaf.notAfter && leaf.notAfter.getTime() < Date.now()) {
		out.error = 'This certificate has already expired';
		return out;
	}

	if (keys.length === 1) {
		out.keyPem = keys[0].pem;
		out.keyStatus = await keyMatchesCert(keys[0], leaf);
		if (out.keyStatus === 'mismatch') {
			out.error = 'That key does not belong to this certificate';
		}
	}
	return out;
}

// Reports whether one san pattern covers a hostname
export function certNameMatches(pattern: string, hostname: string): boolean {
	if (pattern === hostname) return true;
	if (!pattern.startsWith('*.')) return false;
	const dot = hostname.indexOf('.');
	// Wildcards cover exactly one leading label
	return dot > 0 && hostname.slice(dot + 1) === pattern.slice(2);
}

// True when any live certificate covers the hostname
export function hostnameSecured(hostname: string, certs: Certificate[]): boolean {
	const name = hostname.trim().toLowerCase().replace(/\.$/, '');
	if (!name) return false;
	const now = Date.now();
	for (const cert of certs) {
		if (cert.expiresAt && Number(cert.expiresAt.seconds) * 1000 < now) continue;
		for (const pattern of cert.coveredNames) {
			if (certNameMatches(pattern, name)) return true;
		}
	}
	return false;
}

// Web url for a hostname, scheme follows coverage
export function webUrl(hostname: string, port: number, secured: boolean): string {
	const scheme = secured ? 'https' : 'http';
	const defaultPort = secured ? 443 : 80;
	return port && port !== defaultPort
		? `${scheme}://${hostname}:${port}`
		: `${scheme}://${hostname}`;
}

// Upgrades an http url when its host is covered
export function upgradeUrl(url: string, secured: (hostname: string) => boolean): string {
	const m = url.match(/^http:\/\/([^/:?#]+)(?::(\d+))?([/?#].*)?$/);
	if (!m || !secured(m[1].toLowerCase())) return url;
	// Termination rides the same port the url names
	const port = m[2] ? Number(m[2]) : 80;
	const rest = m[3] ?? '';
	return port === 443 ? `https://${m[1]}${rest}` : `https://${m[1]}:${port}${rest}`;
}
