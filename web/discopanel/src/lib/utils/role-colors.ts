// Badge variant type the UI component expects
type BadgeVariant = 'default' | 'secondary' | 'destructive' | 'outline';

const BADGE_VARIANTS: BadgeVariant[] = ['default', 'secondary', 'destructive', 'outline'];

// Hashes any role name onto a badge variant
export function getRoleBadgeVariant(roleName: string): BadgeVariant {
	let hash = 0;
	for (let i = 0; i < roleName.length; i++) {
		hash = ((hash << 5) - hash + roleName.charCodeAt(i)) | 0;
	}
	return BADGE_VARIANTS[Math.abs(hash) % BADGE_VARIANTS.length];
}
