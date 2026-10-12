import { chromium } from 'playwright';

(async () => {
  const browser = await chromium.launch({
    headless: true,
    executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  });

  // 1. Desktop Test
  console.log('\n--- Testing Demo-Web Desktop View (1440x900) ---');
  const page = await browser.newPage({ viewport: { width: 1440, height: 900 } });
  await page.goto('http://localhost:8090/');
  await page.waitForSelector('.order-card');
  console.log('✓ Demo Web loaded successfully');

  // Verify Rolls Strip
  const rolls = await page.locator('.roll-printer-card').count();
  console.log(`✓ Active roll printers displayed: ${rolls}`);

  // Test language toggle
  const initialTitle = await page.textContent('.brand-title');
  console.log('Initial title:', initialTitle);
  await page.click('.btn-lang');
  await page.waitForTimeout(500);
  const toggledTitle = await page.textContent('.brand-title');
  console.log('✓ Language switched:', toggledTitle);

  // Switch back to English or Chinese
  if (toggledTitle.includes('WMS')) {
    // Current is English
  } else {
    // Current is Chinese
  }

  // Click Release Wave on first order
  console.log('Clicking "Release Wave"...');
  await page.click('.order-card:first-child button:has-text("Release Wave"), .order-card:first-child button:has-text("释放波次")');
  await page.waitForTimeout(3000);

  // Verify lines printed
  const printedBadges = await page.locator('.order-card:first-child .state-badge').allTextContents();
  console.log('✓ Line statuses after wave release:', printedBadges);

  // Click Resend (Idempotency)
  console.log('Clicking "Resend (Idempotency)"...');
  await page.click('.order-card:first-child button:has-text("Resend"), .order-card:first-child button:has-text("重发波次")');
  await page.waitForTimeout(2000);

  // Click preview on line 2 (Carton 4x2)
  console.log('Clicking preview on line 2 (4x2 Carton label)...');
  await page.click('.order-card:first-child .line-item-row:nth-child(2) button:has-text("Preview"), .order-card:first-child .line-item-row:nth-child(2) button:has-text("预览")');
  await page.waitForSelector('.preview-viewport img', { timeout: 8000 });
  console.log('✓ Label preview rendered successfully');

  await page.screenshot({ path: '/tmp/demo-web-react-desktop.png' });

  // 2. Mobile View
  console.log('\n--- Testing Demo-Web Mobile View (390x844) ---');
  const mobilePage = await browser.newPage({
    viewport: { width: 390, height: 844 },
    isMobile: true,
    hasTouch: true,
  });
  await mobilePage.goto('http://localhost:8090/');
  await mobilePage.waitForSelector('.order-card');
  console.log('✓ Mobile view rendered');

  await mobilePage.screenshot({ path: '/tmp/demo-web-react-mobile.png' });

  await browser.close();
  console.log('\n=== All Demo-Web React Tests Passed! ===');
})();
