import { rpcClient, silentCallOptions } from '$lib/api/rpc-client';
import { authStore } from '$lib/stores/auth';

export interface UploadProgress {
	sessionId: string;
	bytesUploaded: number;
	totalBytes: number;
	chunksUploaded: number;
	totalChunks: number;
	percentComplete: number;
}

export interface ChunkedUploadOptions {
	chunkSize?: number;
	onProgress?: (progress: UploadProgress) => void;
	signal?: AbortSignal;
	sessionId?: string; // For resume
}

export interface UploadResult {
	sessionId: string;
}

export interface UploadStatus extends UploadProgress {
	missingChunks: number[];
	completed: boolean;
}

const SESSION_STORAGE_KEY = 'chunked_upload_sessions';

// Store session info in localStorage for resume capability
function saveSessionToStorage(sessionId: string, filename: string, totalSize: number): void {
	try {
		const sessions = JSON.parse(localStorage.getItem(SESSION_STORAGE_KEY) || '{}');
		sessions[filename] = { sessionId, totalSize, timestamp: Date.now() };
		localStorage.setItem(SESSION_STORAGE_KEY, JSON.stringify(sessions));
	} catch {
		// Ignore localStorage errors
	}
}

function getSessionFromStorage(filename: string, totalSize: number): string | null {
	try {
		const sessions = JSON.parse(localStorage.getItem(SESSION_STORAGE_KEY) || '{}');
		const session = sessions[filename];
		// Session must exist, match size, and be fresh
		if (
			session &&
			session.totalSize === totalSize &&
			Date.now() - session.timestamp < 4 * 60 * 60 * 1000
		) {
			return session.sessionId;
		}
	} catch {
		// Ignore localStorage errors
	}
	return null;
}

function removeSessionFromStorage(filename: string): void {
	try {
		const sessions = JSON.parse(localStorage.getItem(SESSION_STORAGE_KEY) || '{}');
		delete sessions[filename];
		localStorage.setItem(SESSION_STORAGE_KEY, JSON.stringify(sessions));
	} catch {
		// Ignore localStorage errors
	}
}

// Streams a file upload with resume support
export async function uploadFile(
	file: File,
	options?: ChunkedUploadOptions
): Promise<UploadResult> {
	const onProgress = options?.onProgress;
	const signal = options?.signal;

	let sessionId = options?.sessionId;
	let offset = 0;

	// Check for existing session if not explicitly provided
	if (!sessionId) {
		sessionId = getSessionFromStorage(file.name, file.size) || undefined;
	}

	// Existing sessions get a status check for resume
	if (sessionId) {
		try {
			const status = await getUploadStatus(sessionId);
			if (status.completed) {
				removeSessionFromStorage(file.name);
				return { sessionId };
			}
			offset = status.bytesUploaded;
		} catch {
			// Session expired or invalid, start fresh
			sessionId = undefined;
			offset = 0;
		}
	}

	// Initialize new session if needed
	if (!sessionId) {
		const initResponse = await rpcClient.upload.initUpload(
			{
				filename: file.name,
				totalSize: BigInt(file.size),
				chunkSize: 0 // Not used for streaming uploads
			},
			silentCallOptions
		);
		sessionId = initResponse.sessionId;
		saveSessionToStorage(sessionId, file.name, file.size);
	}

	// Report initial progress
	if (onProgress) {
		onProgress({
			sessionId,
			bytesUploaded: offset,
			totalBytes: file.size,
			chunksUploaded: 0,
			totalChunks: 1,
			percentComplete: file.size > 0 ? (offset / file.size) * 100 : 0
		});
	}

	// Stream upload via single PUT request
	const result = await streamUpload(sessionId, file, offset, onProgress, signal);

	removeSessionFromStorage(file.name);
	return result;
}

// Streams the upload bytes via xhr
function streamUpload(
	sessionId: string,
	file: File,
	offset: number,
	onProgress?: (progress: UploadProgress) => void,
	signal?: AbortSignal
): Promise<UploadResult> {
	return new Promise((resolve, reject) => {
		const xhr = new XMLHttpRequest();
		xhr.open('PUT', `/api/v1/upload/${sessionId}`);
		xhr.setRequestHeader('Content-Type', 'application/octet-stream');

		// Auth headers
		const headers = authStore.getHeaders();
		for (const [key, value] of Object.entries(headers)) {
			xhr.setRequestHeader(key, value as string);
		}

		// Resume offset
		if (offset > 0) {
			xhr.setRequestHeader('X-Upload-Offset', String(offset));
		}

		// Progress tracking via XHR upload events
		xhr.upload.onprogress = (e) => {
			if (onProgress && e.lengthComputable) {
				const bytesUploaded = offset + e.loaded;
				onProgress({
					sessionId,
					bytesUploaded,
					totalBytes: file.size,
					chunksUploaded: 0,
					totalChunks: 1,
					percentComplete: file.size > 0 ? (bytesUploaded / file.size) * 100 : 0
				});
			}
		};

		xhr.onload = () => {
			if (xhr.status >= 200 && xhr.status < 300) {
				try {
					JSON.parse(xhr.responseText);
					resolve({ sessionId });
				} catch {
					reject(new Error('Invalid upload response'));
				}
			} else {
				reject(new Error(`Upload failed: ${xhr.status} ${xhr.statusText}`));
			}
		};

		xhr.onerror = () => reject(new Error('Upload network error'));
		xhr.onabort = () => reject(new Error('Upload cancelled'));

		// Abort signal
		if (signal) {
			if (signal.aborted) {
				xhr.abort();
				return;
			}
			signal.addEventListener('abort', () => xhr.abort(), { once: true });
		}

		// Send the file blob
		const blob = offset > 0 ? file.slice(offset) : file;
		xhr.send(blob);
	});
}

// Checks the status of an upload session
export async function getUploadStatus(sessionId: string): Promise<UploadStatus> {
	const response = await rpcClient.upload.getUploadStatus({ sessionId }, silentCallOptions);

	return {
		sessionId: response.sessionId,
		bytesUploaded: Number(response.bytesReceived),
		totalBytes: Number(response.totalBytes),
		chunksUploaded: response.chunksReceived,
		totalChunks: response.totalChunks,
		percentComplete: (Number(response.bytesReceived) / Number(response.totalBytes)) * 100,
		missingChunks: response.missingChunks,
		completed: response.completed
	};
}

// Cancels an upload session
export async function cancelUpload(sessionId: string): Promise<void> {
	await rpcClient.upload.cancelUpload({ sessionId }, silentCallOptions);
}
