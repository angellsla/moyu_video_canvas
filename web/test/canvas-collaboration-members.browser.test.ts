import { afterAll, beforeAll, expect, test } from "bun:test";
import { existsSync } from "node:fs";
import { chromium, type Browser } from "playwright";

let browser: Browser;
let script: string;

beforeAll(async () => {
    const build = await Bun.build({
        entrypoints: [import.meta.dir + "/fixtures/canvas-collaboration-members-harness.tsx"],
        target: "browser",
        format: "iife",
        define: { "process.env.NODE_ENV": '"production"' },
    });
    if (!build.success) throw new Error(build.logs.join("\n"));
    script = await build.outputs[0].text();
    const executablePath = [process.env.CHROME_PATH, "C:/Program Files/Google/Chrome/Application/chrome.exe", "/Applications/Google Chrome.app/Contents/MacOS/Google Chrome"].find((path): path is string => Boolean(path && existsSync(path)));
    browser = await chromium.launch({ executablePath, headless: true });
}, 60_000);

afterAll(async () => { await browser?.close(); });

for (const mode of ["light", "dark"]) test(`member list toggles, closes outside and supports keyboard dismissal (${mode})`, async () => {
    const page = await browser.newPage({ viewport: { width: 900, height: 600 } });
    try {
        await page.setContent(`<html data-theme="${mode}"><body><div id="root"></div></body></html>`);
        await page.addScriptTag({ content: script });
        const trigger = page.getByRole("button", { name: "协作成员，2 人" });
        const list = page.getByRole("list", { name: "协作成员列表" });
        await trigger.waitFor();
        expect(await trigger.getAttribute("aria-expanded")).toBe("false");
        expect(await list.isVisible()).toBe(false);
        await trigger.click();
        await list.waitFor();
        expect(await trigger.getAttribute("aria-expanded")).toBe("true");
        expect(await list.getByRole("listitem").count()).toBe(2);
        await trigger.click();
        await list.waitFor({ state: "hidden" });
        await trigger.click();
        await list.waitFor();
        await page.getByRole("button", { name: "Outside canvas" }).click();
        await list.waitFor({ state: "hidden" });
        await trigger.focus();
        await page.keyboard.press("Enter");
        await list.waitFor();
        await page.keyboard.press("Escape");
        await list.waitFor({ state: "hidden" });
        expect(await trigger.getAttribute("aria-expanded")).toBe("false");
        expect(await trigger.evaluate((button) => document.activeElement === button)).toBe(true);
    } finally { await page.close(); }
}, 30_000);
