import { test, expect, type Page } from '@playwright/test';

// End-to-end coverage for the mdviewer web UI. The server is auto-started
// by playwright.config.ts (webServer) with the repo root as its --root, so
// README.md, web.go, etc. appear in the file browser.
//
// File rows are <button class="file"> with the name in a <span class="file-name">.
// We match the .file-name span by exact text (the enclosing button has the
// same normalized text, so targeting .file-name avoids strict-mode ambiguity).

function fileRow(page: Page, name: string) {
  const re = new RegExp('^' + name.replace(/[.*+?^${}()|[\]\\]/g, '\\$&') + '$');
  return page.locator('#files .file-name').filter({ hasText: re });
}

// The main preview pane shares the .preview-body class with two hidden
// version-compare panes; exclude those to get a single element.
const PREVIEW = '.preview-body:not(.vcompare-pane-body)';

test.describe('mdviewer web app', () => {
  test('app shell loads with title and file browser', async ({ page }) => {
    await page.goto('/');
    await expect(page).toHaveTitle('mdviewer web preview');
    await expect(page.locator('#appShell')).toBeVisible();
    await expect(page.locator('#files button.file').first()).toBeVisible();
    await expect(page.locator('#cwd')).not.toBeEmpty();
  });

  test('file browser lists known repo files', async ({ page }) => {
    await page.goto('/');
    await expect(fileRow(page, 'README.md')).toBeVisible();
    await expect(fileRow(page, 'web.go')).toBeVisible();
  });

  test('opening a markdown file renders the preview', async ({ page }) => {
    await page.goto('/');
    await fileRow(page, 'README.md').click();
    const body = page.locator(PREVIEW).first();
    await expect(body).toBeVisible();
    // README.md begins with "# mdviewer — Web · Menubar · TUI Markdown Viewer".
    await expect(body.locator('h1').first()).toContainText('mdviewer');
  });

  test('outline tab lists document headings', async ({ page }) => {
    await page.goto('/');
    await fileRow(page, 'README.md').click();
    await expect(page.locator(`${PREVIEW} h1`).first()).toBeVisible();
    await page.locator('#panelTabOutline').click();
    // README has many headings, so the outline should have entries.
    await expect(page.locator('#outlineList > *').first()).toBeVisible();
  });

  test('subfolder browse modal filters by filename', async ({ page }) => {
    await page.goto('/');
    await expect(page.locator('#files button.file').first()).toBeVisible();
    await page.locator('#browseSubfoldersBtn').click();
    await expect(page.locator('#folderBrowseModal')).toBeVisible();
    const fb = page.locator('#fbSearch');
    await expect(fb).toBeVisible();
    await fb.fill('README');
    // Results should include the repo's README files.
    await expect(page.locator('#fbResults')).toContainText('README', { timeout: 10_000 });
  });

  test('lightbox opens for a zoomable image/diagram (best-effort)', async ({ page }) => {
    await page.goto('/');
    await fileRow(page, 'README.md').click();
    await expect(page.locator(PREVIEW).first()).toBeVisible();
    // Let images/diagrams settle, then look for something zoomable.
    await page.waitForTimeout(1500);
    const zoomable = page.locator(`${PREVIEW} img, ${PREVIEW} svg`).first();
    test.skip(await zoomable.count() === 0, 'no zoomable image/diagram in this document');
    await zoomable.scrollIntoViewIfNeeded();
    await zoomable.click();
    // The zoom popup we tuned (wheel-zoom step) should appear.
    await expect(page.locator('#lightbox')).toBeVisible();
    await expect(page.locator('#lightboxScale')).toBeVisible();
    // Native image drag-and-drop must be suppressed so a drag pans only
    // (no translucent "ghost" image). dispatchEvent returns false when a
    // listener called preventDefault.
    const dragPrevented = await page.locator('#lightboxStage > *').first().evaluate((el) => {
      const ev = new Event('dragstart', { bubbles: true, cancelable: true });
      return !el.dispatchEvent(ev);
    });
    expect(dragPrevented).toBe(true);

    // Image annotation: entering draw mode and dragging must create a stroke
    // in the image's SVG overlay (raster images previously no-op'd on draw).
    if (await page.locator('#lightboxStage img').count() > 0) {
      await page.locator('#lbAnnoDrawBtn').click();
      const box = await page.locator('#lightboxStage').boundingBox();
      expect(box).not.toBeNull();
      const cx = box!.x + box!.width / 2;
      const cy = box!.y + box!.height / 2;
      await page.mouse.move(cx - 40, cy - 20);
      await page.mouse.down();
      await page.mouse.move(cx + 30, cy + 15, { steps: 10 });
      await page.mouse.move(cx + 60, cy - 10, { steps: 10 });
      await page.mouse.up();
      await expect(page.locator('#lightboxStage svg.lb-anno-overlay .lb-annotation')).toHaveCount(1);
      // The overlay must be transparent so it never hides the image beneath
      // (regression guard: it otherwise inherits .lightbox-stage's white bg).
      const overlayBg = await page.locator('#lightboxStage svg.lb-anno-overlay')
        .evaluate((el) => getComputedStyle(el).backgroundColor);
      expect(['rgba(0, 0, 0, 0)', 'transparent']).toContain(overlayBg);
      // Rectangle tool: switch to ▭ and drag → an SVG <rect> annotation.
      await page.locator('#lbAnnoRectBtn').click();
      await page.mouse.move(cx - 80, cy - 60);
      await page.mouse.down();
      await page.mouse.move(cx + 80, cy + 60, { steps: 10 });
      await page.mouse.up();
      await expect(page.locator('#lightboxStage svg.lb-anno-overlay rect.lb-annotation')).toHaveCount(1);
      // Saving the annotated image composites the strokes into a PNG download.
      const [download] = await Promise.all([
        page.waitForEvent('download'),
        page.locator('#lbAnnoSaveBtn').click(),
      ]);
      expect(download.suggestedFilename()).toMatch(/image-.*\.png/);
    }
  });
});
