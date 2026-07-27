// @ts-check
import { defineConfig } from 'astro/config';
import starlight from '@astrojs/starlight';
import starlightSidebarTopics from 'starlight-sidebar-topics';

// https://astro.build/config
export default defineConfig({
	site: 'https://docs.discopanel.app',
	integrations: [
		starlight({
			title: 'DiscoPanel',
			customCss: ['./src/styles/custom.css'],
			favicon: '/favicon.png',
			social: [
				{ icon: 'github', label: 'GitHub', href: 'https://github.com/discohaus/discopanel' },
			],
			plugins: [
				starlightSidebarTopics(
					[
						{
							label: 'Documentation',
							link: '/introduction/',
							icon: 'open-book',
							items: [
								{ label: 'Introduction', slug: 'introduction' },
								{
									label: 'Getting Started',
									items: [
										{ label: 'Docker Compose', slug: 'getting-started/docker-compose' },
										{ label: 'Proxmox LXC', slug: 'getting-started/proxmox' },
										{ label: 'Prebuilt Binaries', slug: 'getting-started/prebuilt-binaries' },
										{ label: 'Building from Source', slug: 'getting-started/build-from-source' },
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
										{ label: 'Server Files', slug: 'guides/server-files' },
										{ label: 'Server Backups', slug: 'guides/backups' },
										{ label: 'Tasks & Automation', slug: 'guides/tasks' },
										{ label: 'Modules', slug: 'guides/modules' },
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
							],
						},
						{
							label: 'Development',
							link: '/development/overview/',
							icon: 'seti:go',
							items: [
								{ label: 'Overview', slug: 'development/overview' },
								{ label: 'Contributing', slug: 'contributing' },
								{
									label: 'Panel Internals',
									items: [
										{ label: 'API & Data Model', slug: 'development/api-and-data' },
										{ label: 'Events & Automation', slug: 'development/events' },
										{ label: 'Proxy & Networking', slug: 'development/proxy' },
									],
								},
								{
									label: 'Server Containers',
									items: [
										{ label: 'Provisioning', slug: 'development/provisioning' },
										{ label: 'Runtime Image', slug: 'development/runtime-image' },
										{ label: 'Lifecycle & Health', slug: 'development/lifecycle' },
										{ label: 'Runtime Agent', slug: 'development/agent' },
									],
								},
								{ label: 'API Reference', slug: 'api' },
							],
						},
					],
					{
						exclude: ['/'],
					},
				),
			],
		}),
	],
});
