import { test, expect, Page } from '@playwright/test';
import path from 'node:path';

// One sequential walkthrough that hits every major surface and screenshots
// each step. Tests share state via a serial worker so the project + ticket
// created early are still around when the later steps screenshot the
// detail / completion / search views.
test.describe.configure({ mode: 'serial' });

const SHOTS = path.resolve(__dirname, '..', 'screenshots');

async function shoot(page: Page, name: string) {
  await page.screenshot({
    path: path.join(SHOTS, `${name}.png`),
    fullPage: true,
  });
}

test('00 home (empty)', async ({ page }) => {
  await page.goto('/');
  await shoot(page, '00-home-empty');
});

test('01 projects index (empty)', async ({ page }) => {
  await page.goto('/p');
  await shoot(page, '01-projects-empty');
});

test('02 new project form', async ({ page }) => {
  await page.goto('/p/new');
  await shoot(page, '02-project-new-form');
});

test('03 create project + redirect', async ({ page }) => {
  await page.goto('/p/new');
  await page.fill('input[name="slug"]', 'demo');
  await page.fill('input[name="name"]', 'Demo Project');
  await page.fill('input[name="description"]', 'A short description for the demo');
  await page.fill(
    'textarea[name="summary"]',
    'This is a demo project used by the Playwright walkthrough. The summary needs to be at least two hundred characters long so the create form is accepted; the server enforces this minimum because LLM agents read the summary first when picking up work in the project.',
  );
  await page.click('button[type="submit"]');
  await page.waitForURL(/\/p\/demo$/);
  await shoot(page, '03-project-detail-after-create');
});

test('04 project edit form', async ({ page }) => {
  await page.goto('/p/demo/edit');
  await shoot(page, '04-project-edit-form');
});

test('05 project summary view', async ({ page }) => {
  await page.goto('/p/demo/summary');
  await shoot(page, '05-project-summary-view');
});

test('06 project summary edit (?edit=1)', async ({ page }) => {
  await page.goto('/p/demo/summary?edit=1');
  await shoot(page, '06-project-summary-edit');
});

test('07 phases index (empty)', async ({ page }) => {
  await page.goto('/p/demo/phases');
  await shoot(page, '07-phases-empty');
});

test('08 new phase form', async ({ page }) => {
  await page.goto('/p/demo/phases/new');
  await shoot(page, '08-phase-new-form');
});

test('09 create phase + redirect', async ({ page }) => {
  await page.goto('/p/demo/phases/new');
  await page.fill('input[name="name"]', 'First Phase');
  await page.fill('input[name="description"]', 'The first phase of the demo project');
  await page.fill(
    'textarea[name="summary"]',
    'A phase is a sub-project for larger bodies of work. This phase summary has to be at least two hundred characters long so the create form is accepted; the same minimum applies as on the project summary, because LLM agents context-load this when picking up work in the phase.',
  );
  await page.click('button[type="submit"]');
  await page.waitForURL(/\/p\/demo\/phases\/first-phase$/);
  await shoot(page, '09-phase-detail-after-create');
});

test('10 phases index (populated)', async ({ page }) => {
  await page.goto('/p/demo/phases');
  await shoot(page, '10-phases-populated');
});

test('11 waves view', async ({ page }) => {
  await page.goto('/p/demo/waves');
  await shoot(page, '11-waves');
});

test('12 board (empty)', async ({ page }) => {
  await page.goto('/p/demo/board');
  await shoot(page, '12-board-empty');
});

test('13 new ticket form', async ({ page }) => {
  await page.goto('/p/demo/tickets/new');
  await shoot(page, '13-ticket-new-form');
});

test('14 create ticket + detail', async ({ page }) => {
  await page.goto('/p/demo/tickets/new');
  await page.fill('input[name="title"]', 'First Ticket');
  await page.fill(
    'textarea[name="body"]',
    '## Goal\n\nDemonstrate the ticket detail page.\n\n- supports markdown\n- has a code block\n\n```go\nfmt.Println("hello")\n```\n\nAnd a paragraph at the end.',
  );
  await page.fill('input[name="wave"]', '1');
  await page.click('button[type="submit"]');
  await page.waitForURL(/\/tickets\//);
  await shoot(page, '14-ticket-detail-fresh');
});

test('15 board (populated)', async ({ page }) => {
  await page.goto('/p/demo/board');
  await shoot(page, '15-board-populated');
});

test('16 ticket move (in_progress) + system_move comment', async ({ page }) => {
  await page.goto('/p/demo/board');
  // Click the only ticket card.
  await page.locator('.ticket-card-title').first().click();
  await page.waitForURL(/\/tickets\//);
  await page.selectOption('select[name="target_column"]', 'in_progress');
  await page.fill('textarea[name="comment"]', 'starting work — playwright move');
  // Scope by form action so :has doesn't pick up sibling forms inside the
  // detail page (Move + Complete + Reassign + Comment all share <button type=submit>).
  await page.locator('form[action*="/move"] button[type="submit"]').click();
  await page.waitForURL(/\/tickets\//);
  await shoot(page, '16-ticket-detail-after-move');
});

test('17 ticket edit form', async ({ page }) => {
  await page.goto('/p/demo/board');
  await page.locator('.ticket-card-title').first().click();
  const editLink = page.locator('a:has-text("Edit")').first();
  await editLink.click();
  await page.waitForURL(/\/edit/);
  await shoot(page, '17-ticket-edit-form');
});

test('18 add comment via the form', async ({ page }) => {
  await page.goto('/p/demo/board');
  await page.locator('.ticket-card-title').first().click();
  await page.fill('textarea[name="body"]', 'A human comment from the playwright walkthrough.');
  await page.locator('form.comment-form button[type="submit"]').click();
  // Wait for the htmx swap to complete.
  await page.waitForTimeout(500);
  await shoot(page, '18-ticket-detail-with-comment');
});

test('19 complete the ticket', async ({ page }) => {
  await page.goto('/p/demo/board');
  await page.locator('.ticket-card-title').first().click();
  await page.fill('textarea[name="testing_evidence"]', 'walked the UI manually with playwright');
  await page.fill('textarea[name="work_summary"]', 'demonstrated the completion form end-to-end');
  await page.fill('textarea[name="learnings"]', 'the UI surfaces all the hard rules inline');
  await page.locator('form[action*="/complete"] button[type="submit"]').click();
  await page.waitForURL(/\/tickets\//);
  await shoot(page, '19-ticket-detail-after-complete-frozen');
});

test('20 board (with done ticket)', async ({ page }) => {
  await page.goto('/p/demo/board');
  await shoot(page, '20-board-with-done');
});

test('21 search page (empty query)', async ({ page }) => {
  await page.goto('/search');
  await shoot(page, '21-search-empty-query');
});

test('22 search learnings', async ({ page }) => {
  await page.goto('/search?q=playwright&kind=learnings');
  await shoot(page, '22-search-learnings');
});

test('23 search tickets (no slug → inline error)', async ({ page }) => {
  await page.goto('/search?q=foo&kind=tickets');
  await shoot(page, '23-search-tickets-no-slug');
});

test('24 phase summary edit', async ({ page }) => {
  await page.goto('/p/demo/phases/first-phase/summary?edit=1');
  await shoot(page, '24-phase-summary-edit');
});

test('25 load existing project (picker)', async ({ page }) => {
  await page.goto('/p/load');
  await shoot(page, '25-load-project-picker');
});

test('26 load picker — entered a directory', async ({ page }) => {
  // Navigate the picker via htmx by clicking the "/" crumb so we land on
  // a deterministic root the screenshot can capture.
  await page.goto('/p/load?path=/');
  await shoot(page, '26-load-picker-at-root');
});

test('27 load picker — manual entry expanded', async ({ page }) => {
  await page.goto('/p/load');
  // Open the manual-entry collapsible so the screenshot shows both options.
  await page.locator('details.manual-entry > summary').click();
  await page.waitForTimeout(200);
  await shoot(page, '27-load-picker-manual-entry');
});
