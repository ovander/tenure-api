import { expect, type Page } from '@playwright/test'

// What the flows share: a minimal PDF that passes the upload pipeline (ClamAV,
// MIME sniffing), the CSP watcher, and the dialog confirmation.

export const pdf = (name: string) => ({ name, mimeType: 'application/pdf', buffer: Buffer.from('%PDF-1.7\n1 0 obj<<>>endobj\ntrailer<<>>\n%%EOF\n') })

export const period = new Intl.DateTimeFormat('en-CA', { year: 'numeric', month: '2-digit', timeZone: 'Europe/Paris' }).format(new Date()).slice(0, 7)

// daysAgo formats a past day as the app's DayInput expects it, dd/mm/yyyy.
export function daysAgo(n: number): string {
  const d = new Date(Date.now() - n * 86400000)
  return `${String(d.getDate()).padStart(2, '0')}/${String(d.getMonth() + 1).padStart(2, '0')}/${d.getFullYear()}`
}

// addDiagnostic records one diagnostic from the asset's overview: done a
// week ago (with its result), or not required with a reason.
export async function addDiagnostic(page: Page, kind: string, x: { result?: string; dpe?: string; notRequired?: string }): Promise<void> {
  await page.getByTestId('add-diagnostic').click()
  await page.locator('#diag_kind').click()
  await page.getByRole('option', { name: kind, exact: true }).click()
  if (x.notRequired) {
    await page.locator('#diag_not_required').check()
    await page.locator('#diag_reason').fill(x.notRequired)
  } else {
    await page.locator('#diag_done').fill(daysAgo(7))
    await page.locator('#diag_done').press('Tab')
    if (x.dpe) {
      await page.locator('#diag_result').click()
      await page.getByRole('option', { name: x.dpe, exact: true }).click()
    } else if (x.result) await page.locator('#diag_result').fill(x.result)
  }
  await page.getByTestId('save-diagnostic').click()
  await expect(page.getByTestId('diagnostic-dialog')).toBeHidden()
}

// mail lists what the fake SMTP sink received (e2e/fakes).
export async function mail(page: Page): Promise<{ to: string[]; subject: string }[]> {
  const fakes = process.env.E2E_FAKES_URL
  if (!fakes) throw new Error('E2E_FAKES_URL is not set: run the flows through e2e/run.sh')
  return (await page.request.get(`${fakes}/__e2e/mail`)).json()
}

// watchCSP collects every CSP violation of the page.
export async function watchCSP(page: Page): Promise<string[]> {
  const violations: string[] = []
  page.on('console', (m) => {
    if (/Content Security Policy|Refused to/.test(m.text())) violations.push(m.text())
  })
  return violations
}

// pdfWait is how long a step waits for what a PDF render produces: tenure
// gives gotenberg up to 50 s, within a 60 s client timeout (Q-101), so the
// flows wait a little longer rather than give up first and cancel the render.
export const pdfWait = { timeout: 70_000 }

// confirm accepts the open dialog and waits for it to close, which for an
// issue or a generation is after the documents are rendered.
export async function confirm(page: Page): Promise<void> {
  await page.getByTestId('confirm').click()
  await expect(page.getByTestId('confirm-dialog')).toBeHidden(pdfWait)
}

// state is what the flows pass to one another (they run in order, one worker).
export const state: { assetId?: string; leaseId?: string } = {}
