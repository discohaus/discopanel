import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
import type { Certificate } from '$lib/proto/discopanel/v1/storage_pb';
import { hostnameSecured } from '$lib/certs';

// Loads certificate coverage once for lock glyphs
class CertificatesStore {
	certificates = $state<Certificate[]>([]);
	loaded = $state(false);
	private pending: Promise<void> | null = null;

	// Fetches once, callers may fire and forget
	ensure(): Promise<void> {
		if (this.loaded) return Promise.resolve();
		if (!this.pending) this.pending = this.fetch();
		return this.pending;
	}

	async refresh(): Promise<void> {
		await this.fetch();
	}

	// True when a live certificate covers the hostname
	isSecured(hostname: string): boolean {
		return hostnameSecured(hostname, this.certificates);
	}

	private async fetch(): Promise<void> {
		try {
			const response = await rpcClient.proxy.getCertificates({}, silentCallOptions);
			this.certificates = response.certificates;
			this.loaded = true;
		} catch {
			// Locks simply stay off without read access
		} finally {
			this.pending = null;
		}
	}
}

export const certificatesStore = new CertificatesStore();
