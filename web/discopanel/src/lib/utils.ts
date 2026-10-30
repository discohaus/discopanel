import { clsx, type ClassValue } from 'clsx';
import { twMerge } from 'tailwind-merge';
import type { DockerImage } from './proto/discopanel/v1/minecraft_pb';

export function cn(...inputs: ClassValue[]) {
	return twMerge(clsx(inputs));
}

// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type WithoutChild<T> = T extends { child?: any } ? Omit<T, 'child'> : T;
// eslint-disable-next-line @typescript-eslint/no-explicit-any
export type WithoutChildren<T> = T extends { children?: any } ? Omit<T, 'children'> : T;
export type WithoutChildrenOrChild<T> = WithoutChildren<WithoutChild<T>>;
export type WithElementRef<T, U extends HTMLElement = HTMLElement> = T & { ref?: U | null };

export function formatBytes(bytes: number, decimals = 2): string {
	if (bytes === 0) return '0 Bytes';

	const k = 1024;
	const dm = decimals < 0 ? 0 : decimals;
	const sizes = ['Bytes', 'KB', 'MB', 'GB', 'TB', 'PB', 'EB', 'ZB', 'YB'];

	const i = Math.floor(Math.log(bytes) / Math.log(k));

	return parseFloat((bytes / Math.pow(k, i)).toFixed(dm)) + ' ' + sizes[i];
}

export function getUniqueDockerImages(images: DockerImage[]): DockerImage[] {
	const seen = new Map<string, DockerImage>();
	for (const image of images) {
		if (!seen.has(image.tag)) {
			seen.set(image.tag, image);
		}
	}
	return Array.from(seen.values());
}

export function getDockerImageDisplayName(
	tagOrImage: string | DockerImage,
	dockerImages?: DockerImage[]
): string {
	const image =
		typeof tagOrImage === 'string'
			? dockerImages?.find((img) => img.tag === tagOrImage)
			: tagOrImage;
	if (!image) return tagOrImage as string;
	// Use the displayName field from the generated type
	return image.displayName || image.tag;
}

// Keeps the clicked tab centered while the rail scrolls sideways
export function centerInRail(rail: HTMLElement | null, tab: EventTarget | null | undefined) {
	if (!rail || !(tab instanceof HTMLElement) || rail.scrollWidth <= rail.clientWidth) return;
	const railRect = rail.getBoundingClientRect();
	const tabRect = tab.getBoundingClientRect();
	rail.scrollBy({
		left: tabRect.left - railRect.left - (railRect.width - tabRect.width) / 2,
		behavior: matchMedia('(prefers-reduced-motion: reduce)').matches ? 'auto' : 'smooth'
	});
}

const MAX_ICON_SOURCE_PX = 512;

// Bakes the EXIF rotation into pixels
export async function encodeIconUpload(file: File): Promise<Uint8Array<ArrayBuffer>> {
	const bitmap = await createImageBitmap(file, { imageOrientation: 'from-image' });
	const scale = Math.min(1, MAX_ICON_SOURCE_PX / Math.max(bitmap.width, bitmap.height));
	const canvas = document.createElement('canvas');
	canvas.width = Math.max(1, Math.round(bitmap.width * scale));
	canvas.height = Math.max(1, Math.round(bitmap.height * scale));
	const ctx = canvas.getContext('2d');
	if (!ctx) throw new Error('Canvas is unavailable');
	ctx.drawImage(bitmap, 0, 0, canvas.width, canvas.height);
	bitmap.close();
	const blob = await new Promise<Blob | null>((resolve) => canvas.toBlob(resolve, 'image/png'));
	if (!blob) throw new Error('Could not encode the icon');
	return new Uint8Array(await blob.arrayBuffer());
}
