import { expect, test } from '@playwright/test'
import { addDiagnostic, confirm, mail, pdf, pdfWait, period, watchCSP } from './helpers'

// The phase 1 flows of RENT-FE-001 §4 against a real tenure, in order: each
// builds on what the previous one produced.
test.describe.configure({ mode: 'serial' })

let leaseUrl = ''
let assetUrl = ''
// The income year the flows work on: the latest one whose form 2044 line map
// tenure publishes (read from /params, never written here). The lease starts
// on its 1st of January, so the tax flow has a full year to declare.
let year = 0

test('the owner signs in through Socrate and creates their profile', async ({ page }) => {
  const violations = await watchCSP(page)
  await page.goto('/')
  await expect(page.getByTestId('no-profile')).toBeVisible()
  await page.getByRole('link', { name: 'Créer mon profil' }).click()
  await page.locator('#legal_name').fill('Bailleur E2E')
  await page.locator('#address').fill('1 place Bellecour, 69002 Lyon')
  await page.getByRole('button', { name: 'Créer mon profil' }).click()
  await expect(page.getByRole('button', { name: 'Enregistrer' })).toBeVisible()
  expect(violations).toEqual([])
})

test('flow 1: an asset, a draft lease, its blockers cleared, the contract, activation', async ({ page }) => {
  const violations = await watchCSP(page)
  await page.goto('/')
  await expect(page.getByTestId('welcome')).toBeVisible()
  const params = (await (await page.request.get('/api/v1/params')).json()) as { key: string; value: { tax_year?: number } }[]
  year = Math.max(...params.filter((p) => p.key === 'form_2044_line_map').map((p) => p.value.tax_year ?? 0))
  expect(year).toBeGreaterThan(2000)
  await page.goto('/assets/new')
  await page.locator('#address').fill('12 rue des Lilas, 69003 Lyon')
  await page.locator('#surface').fill('45,5')
  await page.getByTestId('save').click()
  await expect(page.getByTestId('asset-title')).toHaveText('12 rue des Lilas, 69003 Lyon')
  assetUrl = page.url()

  await page.goto(`${assetUrl}/leases`)
  await page.getByTestId('new-lease').click()
  await page.getByTestId('add-tenant').click()
  await page.locator('#tenant_names').fill('Nina Faure')
  await page.locator('#tenant_email').fill('nina@example.invalid')
  await page.getByTestId('save-tenant').click()
  await expect(page.getByTestId('tenant-dialog')).toBeHidden()
  await page.locator('#start_on').fill(`01/01/${year}`)
  await page.locator('#start_on').press('Tab')
  await page.locator('#rent').fill('800')
  await page.locator('#provision').fill('100')
  await page.locator('#deposit').fill('800')
  await page.locator('#irl_quarter').fill(`${year - 1}-Q3`)
  await page.locator('#irl_value').fill('144,51')
  await page.getByTestId('save').click()
  await expect(page.getByTestId('lease-state')).toHaveText('brouillon')
  leaseUrl = page.url().replace(/\/(overview)?$/, '')

  // Blocked, not surprised: every missing diagnostic is listed, generation disabled.
  await expect(page.getByTestId('blockers-generation')).toContainText('diagnostic obligatoire manque')
  // Each names its diagnostic and its asset, never an id, and links to the asset (Q-105).
  const subject = page.getByTestId('blockers-generation').getByTestId('blocker-subject')
  await expect(subject.first()).toHaveText('DPE · 12 rue des Lilas, 69003 Lyon')
  await expect(subject.first()).toHaveAttribute('href', new URL(assetUrl).pathname)
  await expect(page.getByTestId('blockers-generation')).not.toContainText('asset ')
  await expect(page.getByTestId('generate')).toBeDisabled()
  await expect(page.getByTestId('action-activate')).toBeDisabled()

  // The diagnostics, recorded on the asset.
  await page.goto(assetUrl)
  await addDiagnostic(page, 'DPE', { dpe: 'C' })
  await addDiagnostic(page, 'État des risques (ERP)', { result: 'Aucun risque notable' })
  await addDiagnostic(page, 'Électricité', { result: 'Sans anomalie' })
  await addDiagnostic(page, 'Gaz', { notRequired: 'Pas d’installation de gaz' })
  await addDiagnostic(page, 'Plomb (CREP)', { notRequired: 'Immeuble construit après 1949' })
  await addDiagnostic(page, 'Amiante', { notRequired: 'Permis de construire après 1997' })
  await expect(page.getByTestId('diagnostics').locator('tbody tr')).toHaveCount(6)

  // The contract: the deposit first (the contract covers the guarantees), then
  // generation through gotenberg.
  await page.goto(`${leaseUrl}/guarantees`)
  await expect(page.getByTestId('lease-state')).toHaveText('brouillon')
  await expect(page.getByTestId('blockers-generation')).toHaveCount(0)
  await page.getByTestId('add-guarantee').click()
  await expect(page.locator('#g_amount')).toHaveValue('800,00')
  await page.getByTestId('save-guarantee').click()
  await expect(page.getByTestId('guarantees')).toContainText('Dépôt de garantie')
  await page.getByTestId('generate').click()
  await confirm(page)

  // The signed copy, uploaded through ClamAV against the generated contract.
  await page.getByTestId('tab-contract').click()
  await expect(page.getByTestId('document-card').first()).toContainText(/CT-[0-9A-F]{8}-\d{4}-\d{4}/)
  await page.getByTestId('file-signed').setInputFiles(pdf('contrat-signé.pdf'))
  await page.getByTestId('send-signed').click()
  await expect(page.getByText('contrat-signé.pdf')).toBeVisible()

  // The entry inventory, here uploaded as a signed PDF (the capture in
  // Tenure, proposed first since tenure-web v0.16.0, is tested by its own suite).
  await page.getByTestId('tab-inventories').click()
  await page.getByTestId('add-inventory').click()
  await page.locator('#inv_mode').click()
  await page.getByRole('option', { name: 'Déposer le PDF signé' }).click()
  await page.locator('#inv_done').fill(`01/01/${year}`)
  await page.locator('#inv_done').press('Tab')
  await page.getByTestId('file-inventory').setInputFiles(pdf('edl-entree.pdf'))
  await page.getByTestId('save-inventory').click()
  await expect(page.getByTestId('inventories')).toContainText(`01/01/${year}`)

  // Nothing blocks activation any more: the lease becomes active.
  await expect(page.getByTestId('blockers-activation')).toHaveCount(0)
  await page.getByTestId('action-activate').click()
  await confirm(page)
  await expect(page.getByTestId('lease-state')).toHaveText('en cours')
  await expect(page.getByTestId('edit-lease')).toHaveCount(0)
  expect(violations).toEqual([])
})

test('flow 2 and flow 5: the month reviewed and issued, a rent call cancelled and reissued, paid, receipted', async ({ page }) => {
  const violations = await watchCSP(page)
  await page.goto(`/billing/${period}`)
  await expect(page.getByTestId('issue')).toBeDisabled()
  await page.getByTestId('simulate').click()
  const sim = page.getByTestId('simulation')
  await expect(sim).toContainText('Simulation')
  await expect(sim.getByTestId('fee')).toHaveCount(1)
  await expect(sim.getByTestId('fees-total')).toHaveText(/900,00\s€/)

  // Issue: the dialog restates count and total; the call is e-mailed.
  await page.getByTestId('issue').click()
  await expect(page.getByTestId('confirm-dialog')).toContainText('1 appel(s) de loyer')
  await expect(page.getByTestId('confirm-dialog')).toContainText(/900,00\s€/)
  await confirm(page)
  await expect(page.getByTestId('doc-number')).toHaveText([/^AL-/])
  await expect.poll(async () => (await mail(page)).some((m) => m.to.includes('nina@example.invalid'))).toBe(true)

  // Flow 5 from the archive: the rent call is cancelled and reissued, with a reason.
  await page.goto('/documents?class=rent_call')
  await expect(page.getByTestId('doc-class')).toHaveText(['Appel de loyer'])
  await page.getByTestId('archive-cancel').click()
  await expect(page.getByTestId('confirm-dialog')).toContainText('document d’annulation')
  await expect(page.getByTestId('confirm')).toBeDisabled()
  await page.locator('#archive_reason').fill('Erreur sur le libellé')
  await page.getByTestId('confirm').click()
  await expect(page.getByText(/réémis sous le numéro AL-/)).toBeVisible(pdfWait)
  await page.goto(`/billing/${period}`)
  await expect(page.getByTestId('doc-kind')).toContainText(['Appel de loyer', 'Annulation', 'Appel de loyer'])
  await expect(page.getByTestId('documents')).toContainText('émission 2')

  // A payment of what the lease owes, applied automatically; then the receipt.
  await page.getByTestId('to-payments').click()
  await page.getByTestId('add-payment').click()
  await page.locator('#pay_lease').click()
  await page.getByRole('option', { name: /Lilas/ }).click()
  await expect(page.locator('#pay_amount')).toHaveValue('900,00')
  await page.getByTestId('save-payment').click()
  await expect(page.getByTestId('payment-dialog')).toBeHidden()
  await expect(page.getByTestId('payments')).toContainText(/900,00\s€/)
  await page.getByTestId('issue-receipts').click()
  await expect(page.getByText('Quittances émises')).toBeVisible(pdfWait)
  await page.goto(`/billing/${period}`)
  await expect(page.getByTestId('documents')).toContainText('Quittance')
  await expect(page.getByTestId('doc-number')).toContainText([/^QL-/])
  await page.goto(`${leaseUrl}/billing`)
  await expect(page.getByTestId('arrears-none')).toBeVisible()
  expect(violations).toEqual([])
})

test('flow 3: a syndic statement imported, the exercise reconciled, the RG statement issued, a new provision', async ({ page }) => {
  const violations = await watchCSP(page)
  const assetId = assetUrl.split('/').at(-1)
  await page.goto(`/charges?asset=${assetId}&year=${year}`)
  await expect(page.getByTestId('charges-empty')).toContainText(String(year))

  // Import the statement (uploaded through ClamAV): its lines arrive as drafts.
  await page.getByTestId('import').click()
  await expect(page.locator('#im_on')).toHaveValue(`31/12/${year}`)
  await page.getByTestId('file-im_csv').setInputFiles({ name: `decompte-${year}.csv`, mimeType: 'text/csv',
    buffer: Buffer.from('Libellé;Montant;Récupérable\nEau froide;412,30;412,30\nHonoraires syndic;1050,00;0\n') })
  await page.getByTestId('run-import').click()
  await expect(page.getByTestId('import-dialog')).toBeHidden()
  await expect(page.getByTestId('charge-status')).toHaveText(['Brouillon', 'Brouillon'])
  await expect(page.getByTestId('start-recon')).toBeDisabled()
  for (const label of ['Eau froide', 'Honoraires syndic']) {
    // Paid to the syndic at the exercise's end: the tax summary is cash basis.
    await page.getByTestId('charge-label').filter({ hasText: label }).click()
    await page.locator('#ch_paid').fill(`31/12/${year}`)
    await page.locator('#ch_paid').press('Tab')
    await page.getByTestId('confirm-charge').click()
    await expect(page.getByTestId('charge-dialog')).toBeHidden()
  }
  await page.locator('#f_status').click()
  await page.getByRole('option', { name: 'Tous' }).click()
  await expect(page.getByTestId('charge-status')).toHaveText(['Validée', 'Validée'])
  await expect(page.getByTestId('charges-totals')).toContainText(/Récupérable 412,30\s€/)

  // The wizard: allocation over the whole exercise, balances, provisions, issue.
  await page.getByTestId('start-recon').click()
  await expect(page.locator('#ex_start')).toHaveValue(`01/01/${year}`)
  await page.getByTestId('confirm').click()
  await expect(page).toHaveURL(/\/reconciliations\/[^/?]+$/)
  await expect(page.getByTestId('recon-recoverable')).toHaveText(/412,30\s€/)
  await page.getByTestId('allocate').click()
  await expect(page.getByTestId('recon-state')).toHaveText('répartie')
  await expect(page.getByTestId('allocation-line')).toContainText('Nina Faure')
  await expect(page.getByTestId('allocation-line')).toContainText(/412,30\s€/)
  await page.getByTestId('next').click()
  // No rent call was issued in that exercise: the tenant owes the whole share.
  await expect(page.getByTestId('balance')).toHaveText(/Le locataire doit 412,30\s€/)
  await page.getByTestId('next').click()
  const line = page.getByTestId('provision-line')
  await expect(line).toContainText('Proposée')
  await line.locator('input[type=checkbox]').check()
  await expect(line.locator('input[type=checkbox]')).toBeChecked()
  await page.getByTestId('next').click()
  await expect(page.getByTestId('to-write')).toContainText('Provision portée à')
  await page.getByTestId('issue-recon').click()
  await expect(page.getByTestId('confirm-dialog')).toContainText('1 décompte(s)')
  await page.getByTestId('confirm').click()
  await expect(page.getByTestId('recon-state')).toHaveText('émise', pdfWait)
  await expect(page.getByTestId('statements')).toContainText(/RG-[0-9A-F]{8}-\d{4}-\d{4}/)
  expect(violations).toEqual([])
})

test('flow 4: the tax year computed after step-up, its 2044 lines reviewed, issued with its PDF and CSV', async ({ page }) => {
  const violations = await watchCSP(page)
  // The year's property tax, paid in October and kept without its notice.
  await page.goto(`/charges?asset=${assetUrl.split('/').at(-1)}&year=${year}`)
  await page.getByTestId('add-charge').click()
  await page.locator('#ch_category').click()
  await page.getByRole('option', { name: 'Taxe foncière', exact: true }).click()
  await page.locator('#ch_label').fill(`Taxe foncière ${year}`)
  await page.locator('#ch_amount').fill('850')
  await page.locator('#ch_on').fill(`15/10/${year}`)
  await page.locator('#ch_on').press('Tab')
  await page.locator('#ch_paid').fill(`15/10/${year}`)
  await page.locator('#ch_paid').press('Tab')
  await page.getByTestId('save-charge').click()
  await expect(page.getByTestId('charge-dialog')).toBeHidden()
  await expect(page.getByTestId('charge-label').filter({ hasText: 'Taxe foncière' })).toBeVisible()

  await page.goto(`/tax/${year}`)
  await expect(page.getByTestId('tax-title')).toHaveText(`Revenus fonciers ${year}`)
  // The fake Socrate signs in with a fresh MFA authentication: step-up passes.
  await page.getByTestId('compute').click()
  await expect(page.getByTestId('tax-state')).toHaveText('brouillon')
  // The property tax is on its 2044 line, in orange for its missing notice,
  // and opens on its source. The syndic statement's lines are on no line.
  const tax = page.getByTestId('line').filter({ hasText: /850,00\s€/ })
  await expect(tax).toHaveCount(1)
  await expect(tax).toContainText('Taxe foncière')
  await expect(tax).toHaveClass(/bg-orange-50/)
  await expect(page.getByTestId('tax-flags')).toContainText('1 charge(s) sans justificatif')
  await tax.click()
  await expect(page.getByTestId('source')).toContainText([/Taxe foncière/])
  await page.getByTestId('issue-tax').click()
  await page.getByTestId('confirm').click()
  await expect(page.getByTestId('tax-state')).toHaveText('émis', pdfWait)
  await expect(page.getByTestId('tax-documents').getByTestId('document-card')).toHaveCount(2)
  await expect(page.getByTestId('tax-documents')).toContainText(`Récapitulatif ${year} (PDF)`)
  await expect(page.getByTestId('tax-documents')).toContainText(`Lignes ${year} (CSV)`)
  for (const id of ['compute', 'issue-tax']) await expect(page.getByTestId(id)).toHaveCount(0)

  // Everything the flows produced is in the archive.
  await page.goto('/documents')
  for (const c of ['Contrat', 'Quittance', 'Décompte de régularisation', 'Récapitulatif fiscal'])
    await expect(page.getByTestId('doc-class').filter({ hasText: c }).first()).toBeVisible()
  // A cancellation is archived in the class of what it cancels (Q-49): the
  // rent call, its cancellation and the reissued call.
  await expect(page.getByTestId('doc-class').filter({ hasText: 'Appel de loyer' })).toHaveCount(3)
  expect(violations).toEqual([])
})
