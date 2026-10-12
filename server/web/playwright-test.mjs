import { chromium } from 'playwright';

async function run() {
  console.log('Launching browser with Playwright...');
  const browser = await chromium.launch({
    headless: true,
    executablePath: '/Applications/Google Chrome.app/Contents/MacOS/Google Chrome',
  });

  // 1. Desktop Test
  console.log('\n--- Testing Desktop View (1440x900) ---');
  const desktopContext = await browser.newContext({
    viewport: { width: 1440, height: 900 },
  });
  const page = await desktopContext.newPage();

  await page.goto('http://localhost:8080/ui/');

  // Login
  await page.fill('#token-input', 'dev-key-ops-0123456789abcdef');
  await page.click('button[type="submit"]');
  await page.waitForSelector('.topbar');
  console.log('✓ Successfully logged in');

  // Verify Overview
  await page.waitForSelector('.stats-cards');
  console.log('✓ Overview stats loaded');

  // Ensure language is Chinese for testing
  const currentLang = await page.textContent('.lang-text');
  if (currentLang.includes('EN')) {
    await page.click('.btn-lang');
    await page.waitForTimeout(500);
  }
  const brandZh = await page.textContent('.brand-text');
  console.log('✓ Current brand title:', brandZh);

  // Navigate to Printers View (works with either Chinese or English label)
  const printersTab = page.locator('.nav-tab:has-text("打印机"), .nav-tab:has-text("Printers")');
  await printersTab.click();
  await page.waitForSelector('.data-table');
  console.log('✓ Navigated to Printers view');

  // Click details on the first printer
  const firstDetailsBtn = page.locator('.table-btn-group button:has-text("详情"), .table-btn-group button:has-text("Details")').first();
  if (await firstDetailsBtn.count()) {
    await firstDetailsBtn.click();
    await page.waitForSelector('.modal-card');
    console.log('✓ Opened printer detail modal');

    // Click "编辑属性" or "Edit Attributes"
    await page.click('button:has-text("编辑属性"), button:has-text("Edit Attributes")');
    console.log('✓ Entered edit mode');

    // Fill Custom Label & Remark
    await page.fill('input[placeholder*="2号包装台"], input[placeholder*="Packing Station 2"]', '1号加急台');
    await page.fill('textarea[placeholder*="4x6热敏纸"], textarea[placeholder*="4x6 thermal paper"]', '测试备注: 靠近西侧分拣口');
    await page.click('button:has-text("保存修改"), button:has-text("Save Changes")');

    await page.waitForSelector('.toast-success');
    console.log('✓ Saved custom_label and remark successfully');

    // Close modal
    await page.click('.modal-close-btn');
  }

  // Go to Preview View
  await page.click('.nav-tab:has-text("模板预览"), .nav-tab:has-text("Preview")');
  await page.waitForSelector('.zpl-textarea');

  // Type custom text in Preview editor
  await page.fill('.zpl-textarea', '^XA^FO50,50^A0N,50,50^FDPLAYWRIGHT_INTL_TEST^FS^XZ');
  console.log('✓ Typed custom ZPL in editor');

  // Click Render Preview
  await page.click('button:has-text("渲染预览"), button:has-text("Render Preview")');
  await page.waitForSelector('.preview-image-output', { timeout: 10000 });
  console.log('✓ ZPL successfully compiled and rendered');

  // Wait 6 seconds to ensure background polling does NOT overwrite or reset the editor
  console.log('Waiting 6s to verify auto-refresh does not wipe input...');
  await page.waitForTimeout(6000);
  const currentZpl = await page.inputValue('.zpl-textarea');
  if (currentZpl.includes('PLAYWRIGHT_INTL_TEST')) {
    console.log('✓ VERIFIED: Editor content preserved across background auto-refresh!');
  } else {
    throw new Error('FAILED: Editor was wiped by auto-refresh!');
  }

  await page.screenshot({ path: '/tmp/desktop-preview.png', fullPage: true });

  // 2. Mobile View Test (iPhone 14: 390x844)
  console.log('\n--- Testing Mobile View (390x844) ---');
  const mobileContext = await browser.newContext({
    viewport: { width: 390, height: 844 },
    userAgent:
      'Mozilla/5.0 (iPhone; CPU iPhone OS 16_5 like Mac OS X) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/16.5 Mobile/15E148 Safari/604.1',
    isMobile: true,
    hasTouch: true,
  });

  const mobilePage = await mobileContext.newPage();
  // Set auth token in localStorage
  await mobilePage.addInitScript(() => {
    localStorage.setItem('cps.token', 'dev-key-ops-0123456789abcdef');
    localStorage.setItem('cps.locale', 'zh-Hans');
  });

  await mobilePage.goto('http://localhost:8080/ui/#overview');
  await mobilePage.waitForSelector('.mobile-bottom-bar');
  console.log('✓ Mobile bottom navigation island rendered');

  // Test mobile bottom bar navigation to Printers
  await mobilePage.click('.mobile-tab-btn:has-text("打印机"), .mobile-tab-btn:has-text("Printers")');
  await mobilePage.waitForSelector('.mobile-card');
  console.log('✓ Mobile touch-friendly card list rendered for printers');

  await mobilePage.screenshot({ path: '/tmp/mobile-printers.png' });

  // Open detail on mobile (should open as bottom sheet with grabber)
  await mobilePage.locator('.mobile-card').first().click();
  await mobilePage.waitForSelector('.ios-grabber');
  console.log('✓ Mobile bottom sheet opened with iOS grabber handle');

  await mobilePage.screenshot({ path: '/tmp/mobile-sheet.png' });

  await browser.close();
  console.log('\n=== All Playwright UI Tests Passed Successfully! ===');
}

run().catch((e) => {
  console.error(e);
  process.exit(1);
});
