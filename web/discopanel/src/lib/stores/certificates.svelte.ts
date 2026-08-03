import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
import type { Certificate } from '$lib/proto/discopanel/v1/storage_pb';
import { TlsProvider } from '$lib/proto/discopanel/v1/storage_pb';
import { hostnameSecured } from '$lib/certs';

// Loads https settings and coverage once for scheme glyphs
class CertificatesStore {
	certificates = $state<Certificate[]>([]);
	httpsEnabled = $state(false);
	tlsProvider = $state<TlsProvider>(TlsProvider.DNS);
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

	// True when the panel serves its own certificates
	get panelTerminates(): boolean {
		return this.httpsEnabled && this.tlsProvider === TlsProvider.DISCOPANEL;
	}

	// True when the hostname answers over https
	isSecured(hostname: string): boolean {
		if (!this.httpsEnabled) return false;
		// An edge covers every name it fronts
		if (!this.panelTerminates) return true;
		return hostnameSecured(hostname, this.certificates);
	}

	private async fetch(): Promise<void> {
		try {
			const [certs, status] = await Promise.all([
				rpcClient.proxy.getCertificates({}, silentCallOptions),
				rpcClient.proxy.getProxyStatus({}, silentCallOptions)
			]);
			this.certificates = certs.certificates;
			this.httpsEnabled = status.httpsEnabled;
			this.tlsProvider = status.tlsProvider;
			this.loaded = true;
		} catch {
			// Scheme glyphs stay off without read access
		} finally {
			this.pending = null;
		}
	}
}

export const certificatesStore = new CertificatesStore();
