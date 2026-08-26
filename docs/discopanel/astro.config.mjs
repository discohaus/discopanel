// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';

// https://astro.build/config
export default defineConfig({
	site: 'https://docs.discopanel.app',
	vite: {
		server: { fs: { allow: ['../..'] } },
	},
	integrations: [
		starlight({
			title: 'DiscoPanel',
			customCss: ['./src/styles/custom.css'],
			favicon: '/favicon.svg',
			components: {
				SiteTitle: './src/components/SiteTitle.astro',
			},
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/discohaus/discopanel' },
			],
			sidebar: [
				{ label: 'Introduction', slug: 'introduction' },
				{
					label: 'Getting Started',
					items: [
						{ label: 'Docker Compose', slug: 'getting-started/docker-compose' },
						{ label: 'Proxmox LXC', slug: 'getting-started/proxmox' },
						{ label: 'Prebuilt Binaries', slug: 'getting-started/prebuilt-binaries' },
						{ label: 'Building from Source', slug: 'getting-started/build-from-source' },
						{ label: 'Upgrading from v2', slug: 'getting-started/upgrade-v2' },
					],
				},
				{ label: 'Configuration', slug: 'configuration' },
				{
					label: 'Guides',
					items: [
						{ label: 'Server Software', slug: 'guides/server-software' },
						{ label: 'Modpacks', slug: 'guides/modpacks' },
						{ label: 'Server Performance', slug: 'guides/performance' },
						{ label: 'Auto-Pause & Auto-Stop', slug: 'guides/autopause' },
						{ label: 'Proxy & Domains', slug: 'guides/proxy' },
						{ label: 'The Lobby', slug: 'guides/lobby' },
						{ label: 'HTTPS & Certificates', slug: 'guides/tls' },
						{ label: 'Server Files', slug: 'guides/server-files' },
						{ label: 'Server Backups', slug: 'guides/backups' },
						{ label: 'Tasks & Automation', slug: 'guides/tasks' },
						{
							label: 'Modules',
							items: [
								{ label: 'Overview', slug: 'guides/modules' },
								{ label: 'Geyser', slug: 'guides/modules/geyser' },
								{ label: 'BlueMap', slug: 'guides/modules/bluemap' },
								{ label: 'Status Panel', slug: 'guides/modules/status' },
								{ label: 'Prometheus Exporter', slug: 'guides/modules/exporter' },
								{ label: 'Playit.gg', slug: 'guides/modules/playit' },
								{ label: 'Steam Bridge', slug: 'guides/modules/steambridge' },
								{ label: 'Doctor', slug: 'guides/modules/doctor' },
								{ label: 'Discord Bot', slug: 'guides/modules/bot' },
							],
						},
						{ label: 'Users & Roles', slug: 'guides/users' },
						{
							label: 'OIDC',
							items: [
								{ label: 'Keycloak', slug: 'guides/oidc/keycloak' },
								{ label: 'Authelia', slug: 'guides/oidc/authelia' },
								{ label: 'Google', slug: 'guides/oidc/google' },
								{ label: 'Discord', slug: 'guides/oidc/discord' },
							],
						},
					],
				},
				{ label: 'FAQ', slug: 'faq' },
				{ label: 'Troubleshooting', slug: 'troubleshooting' },
				{ label: 'Contributing', slug: 'contributing' },
				{ label: 'API Reference', slug: 'api' },
			],
		}),
	],
});
