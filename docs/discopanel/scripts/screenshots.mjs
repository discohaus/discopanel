#!/usr/bin/env node
// Captures UI screenshots from a running DiscoPanel for the docs
// Usage PANEL_URL=http://localhost:8080 PANEL_USER=admin PANEL_PASS=secret node scripts/screenshots.mjs [name...]
import puppeteer, { Page } from 'puppeteer-core';
import sharp from 'sharp';
import { mkdir, writeFile } from 'node:fs/promises';
import { dirname, join } from 'node:path';
import { fileURLToPath } from 'node:url';

/**
 * @typedef {import('puppeteer-core').Page} Page
 */

const BASE = process.env.PANEL_URL || 'http://localhost:8080';
const USER = process.env.PANEL_USER || 'admin';
const PASS = process.env.PANEL_PASS || '';
const CHROME = process.env.CHROME_BIN || '/usr/bin/chromium';
const OUT = join(dirname(fileURLToPath(import.meta.url)), '..', 'src', 'assets', 'screenshots');

const only = process.argv.slice(2);
const sleep = (ms) => new Promise((r) => setTimeout(r, ms));

/**
 * Clicks a text element matching the given selector and text.
 * @param {Page} page The Puppeteer page object.
 * @param {string} selector The CSS selector to match elements.
 * @param {string} text The text to match within the elements.
 * @returns {Promise<void>}
 * @throws {Error} if no matching element is found
 */
async function clickText(page, selector, text) {
  for (const h of await page.$$(selector)) {
    const t = await h.evaluate((e) => e.textContent.trim());
    if (t.toLowerCase().includes(text.toLowerCase())) {
      await h.evaluate((e) => e.scrollIntoView({ block: 'center' }));
      await sleep(200);
      await h.click();
      return;
    }
  }
  throw new Error(`no ${selector} matching "${text}"`);
}

/**
 * Navigate to a server tab and wait for it to load
 * @param {Page} page The Puppeteer page object.
 * @param {string} serverUrl The URL of the server page.
 * @param {string} tab The name of the tab to navigate to.
 */
async function goTab(page, serverUrl, tab) {
  await page.goto(serverUrl, { waitUntil: 'networkidle2' });
  await sleep(1200);
  await clickText(page, 'button', tab);
  await sleep(1800);
}

/**
 * Login to the panel, creating an admin account if necessary
 * @param {Page} page The Puppeteer page object.
 * @returns {Promise<void>}
 */
async function login(page) {
  await page.goto(BASE + '/login', { waitUntil: 'networkidle2' });
  await sleep(800);
  if (!page.url().includes('/login')) return;
  const text = await page.evaluate(() => document.body.innerText);
  if (text.includes('Create admin account')) {
    await page.type('#admin-username', USER);
    await page.type('#admin-password', PASS);
    await page.type('#admin-confirm', PASS);
    await clickText(page, 'button', 'Create admin account');
  } else {
    await page.type('input[type=text]', USER);
    await page.type('input[type=password]', PASS);
    await clickText(page, 'button', 'Sign in');
  }
  await page.waitForFunction(() => !location.pathname.includes('/login'), { timeout: 20000 });
  await sleep(1000);
}

/**
 * Returns the URL of the first server in the list, or throws if none exist
 * @param {Page} page The Puppeteer page object.
 * @returns {Promise<string|null>}
 */
async function firstServerUrl(page) {
  await page.goto(BASE + '/servers', { waitUntil: 'networkidle2' });
  await sleep(1500);
  const href = await page.evaluate(() => {
    const a = [...document.querySelectorAll('a[href^="/servers/"]')].find(
      (e) => !e.getAttribute('href').endsWith('/new')
    );
    return a ? a.getAttribute('href') : null;
  });
  if (!href) throw new Error('no servers exist, create one first');
  return BASE + href;
}

/**
 * Collection of screenshot capture functions for different pages and states of the DiscoPanel UI.
 * @type {Record<string, (page: Page, srv: string) => Promise<void>}
 */
const shots = {
  async home(page) {
    await page.goto(BASE + '/', { waitUntil: 'networkidle2' });
    await sleep(2000);
  },
  async 'create-server'(page) {
    await page.goto(BASE + '/servers/new', { waitUntil: 'networkidle2' });
    await sleep(1500);
    await page.type('#name', 'Skyblock Party');
    await clickText(page, 'button', 'Select a mod loader');
    await sleep(500);
    await clickText(page, '[role=option]', 'Paper');
    await sleep(1000);
  },
  async 'server-overview'(page, srv) {
    await page.goto(srv, { waitUntil: 'networkidle2' });
    await sleep(2500);
  },
  async 'server-console'(page, srv) {
    await goTab(page, srv, 'Console');
  },
  async 'server-files'(page, srv) {
    await goTab(page, srv, 'Files');
  },
  async 'server-tasks'(page, srv) {
    await goTab(page, srv, 'Tasks');
  },
  async 'task-backup'(page, srv) {
    await goTab(page, srv, 'Tasks');
    await clickText(page, 'button', 'New task');
    await sleep(1000);
    await page.type('#taskName', 'Nightly backup');
    await clickText(page, '[role=dialog] button', 'Command');
    await sleep(500);
    await clickText(page, '[role=option]', 'Backup');
    await sleep(1000);
  },
  async 'path-picker'(page, srv) {
    await goTab(page, srv, 'Tasks');
    await clickText(page, 'button', 'New task');
    await sleep(1000);
    await clickText(page, '[role=dialog] button', 'Command');
    await sleep(500);
    await clickText(page, '[role=option]', 'Backup');
    await sleep(800);
    await page.click('[role=dialog] button[title=Browse]');
    await sleep(1500);
  },
  async 'server-modules'(page, srv) {
    await goTab(page, srv, 'Modules');
  },
  async 'server-network'(page, srv) {
    await goTab(page, srv, 'Network');
  },
  async 'server-properties'(page, srv) {
    await goTab(page, srv, 'Properties');
  },
  async 'server-properties-enableProxyProtocol'(page, srv) {
    await goTab(page, srv, 'Properties');
    await clickText(page, 'span', 'Proxy');
    await sleep(1000);
    await clickText(page, 'button#enableProxyProtocol', '')
    await sleep(1000);
  },
  async 'server-settings'(page, srv) {
    await goTab(page, srv, 'Settings');
  },
  async 'modules-page'(page) {
    await page.goto(BASE + '/modules', { waitUntil: 'networkidle2' });
    await sleep(2000);
  },
  async modpacks(page) {
    await page.goto(BASE + '/modpacks', { waitUntil: 'networkidle2' });
    await sleep(4000);
  },
  async 'settings-network'(page) {
    await page.goto(BASE + '/settings?tab=network', { waitUntil: 'networkidle2' });
    await sleep(3000);
  },
  async 'listener-inspector'(page) {
    await page.goto(BASE + '/settings?tab=network', { waitUntil: 'networkidle2' });
    await sleep(2000);
    await clickText(page, 'p', 'Primary');
    await sleep(2000);
    let element = await page.waitForSelector('label#use-ingress-proxy');
    element.click()
    element = await page.waitForSelector("div#trusted-proxies");
    await element.scrollIntoView();
    await sleep(1500);
  },
  async 'settings-users'(page) {
    await page.goto(BASE + '/settings?tab=users', { waitUntil: 'networkidle2' });
    await sleep(2000);
  },
  async 'settings-roles'(page) {
    await page.goto(BASE + '/settings?tab=roles', { waitUntil: 'networkidle2' });
    await sleep(2000);
  },
  async 'role-editor'(page) {
    await page.goto(BASE + '/settings?tab=roles', { waitUntil: 'networkidle2' });
    await sleep(2000);
    await clickText(page, 'button', 'Edit');
    await sleep(1500);
  },
  async 'settings-auth'(page) {
    await page.goto(BASE + '/settings?tab=auth', { waitUntil: 'networkidle2' });
    await sleep(2000);
  },
};

/**
 * Swaps public IPv4s for a documentation address before capture
 * @param {Page} page The Puppeteer page object.
 */
async function scrubPublicIPs(page) {
  await page.evaluate(() => {
    const priv = /^(10\.|127\.|192\.168\.|172\.(1[6-9]|2\d|3[01])\.|169\.254\.|0\.)/;
    const re = /\b((?:\d{1,3}\.){3}\d{1,3})\b/g;
    const walker = document.createTreeWalker(document.body, NodeFilter.SHOW_TEXT);
    let node;
    while ((node = walker.nextNode())) {
      if (!re.test(node.nodeValue)) continue;
      node.nodeValue = node.nodeValue.replace(re, (ip) => (priv.test(ip) ? ip : '203.0.113.7'));
    }
  });
}

const browser = await puppeteer.launch({
  executablePath: CHROME,
  args: ['--no-sandbox', '--window-size=1600,1000', '--force-device-scale-factor=2'],
  defaultViewport: { width: 1600, height: 1000, deviceScaleFactor: 2 },
});
const page = await browser.newPage();
await mkdir(OUT, { recursive: true });
await login(page);
const srv = await firstServerUrl(page);

let failed = 0;
for (const [name, run] of Object.entries(shots)) {
  if (only.length && !only.includes(name)) continue;
  try {
    await run(page, srv);
    await scrubPublicIPs(page);
    const raw = await page.screenshot();
    const png = await sharp(raw).png({ palette: true, quality: 90 }).toBuffer();
    await writeFile(join(OUT, `${name}.png`), png);
    console.log(`ok   ${name} (${Math.round(png.length / 1024)} KB)`);
  } catch (err) {
    failed++;
    console.error(`FAIL ${name}: ${err.message}`);
  }
}
await browser.close();
process.exit(failed ? 1 : 0);
