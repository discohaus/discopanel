import type { ModuleConfigField } from '$lib/proto/discopanel/v1/storage_pb';
import { ModuleConfigFieldType, ModuleConfigSeverity } from '$lib/proto/discopanel/v1/storage_pb';

export interface ConfigFieldIssue {
	message: string;
	severity: ModuleConfigSeverity;
}

// Mirrors backend envTruthy semantics
function envTruthy(value: string | undefined): boolean {
	const v = (value ?? '').trim().toLowerCase();
	return v !== '' && v !== 'false' && v !== '0';
}

// Falls back to warn when severity is unspecified
export function fieldSeverity(field: ModuleConfigField): ModuleConfigSeverity {
	return field.severity === ModuleConfigSeverity.UNSPECIFIED
		? ModuleConfigSeverity.WARN
		: field.severity;
}

// Mirrors backend checks, alias values defer to the server
export function evaluateConfigField(
	field: ModuleConfigField,
	values: Record<string, string>
): ConfigFieldIssue | null {
	const val = (values[field.env] ?? '').trim();
	const severity = fieldSeverity(field);
	let required = field.required;
	if (required && field.requiredUnless && envTruthy(values[field.requiredUnless])) {
		required = false;
	}
	if (!val) {
		return required ? { message: `${field.env} is required`, severity } : null;
	}
	if (val.includes('{{')) return null;
	if (field.type === ModuleConfigFieldType.INT) {
		const n = Number(val);
		if (!Number.isInteger(n)) {
			return { message: `${field.env} must be a number`, severity };
		}
		if (field.min !== undefined && n < field.min) {
			return { message: `${field.env} must be at least ${field.min}`, severity };
		}
		if (field.max !== undefined && n > field.max) {
			return { message: `${field.env} must be at most ${field.max}`, severity };
		}
	} else if (field.type === ModuleConfigFieldType.BOOL) {
		if (val !== 'true' && val !== 'false') {
			return { message: `${field.env} must be true or false`, severity };
		}
	} else if (field.type === ModuleConfigFieldType.SELECT) {
		if (!field.options.some((o) => o.value === val)) {
			return { message: `${field.env} must be one of the listed options`, severity };
		}
	}
	if (field.regex) {
		try {
			if (!new RegExp(field.regex).test(val)) {
				return {
					message: field.regexMessage || `${field.env} does not match the required format`,
					severity
				};
			}
		} catch {
			// Server rejects uncompilable patterns at template save
		}
	}
	return null;
}

// Buckets fields by group preserving declaration order
export function groupedConfigFields(fields: ModuleConfigField[]): [string, ModuleConfigField[]][] {
	const groups: [string, ModuleConfigField[]][] = [];
	for (const field of fields) {
		if (!field.env) continue;
		const name = field.group || '';
		const existing = groups.find(([g]) => g === name);
		if (existing) existing[1].push(field);
		else groups.push([name, [field]]);
	}
	return groups;
}
