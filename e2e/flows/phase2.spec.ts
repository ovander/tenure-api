import { expect, test, type Page } from '@playwright/test'
import { addDiagnostic, confirm, pdf, pdfWait, watchCSP } from './helpers'

// The inventory lifecycle of RENT-INV-001 against a real tenure, in order
// (slice 11, the rehearsal of the phase 2 gate; Q-117): one lease taken in the
// browser from the asset's layout to the deposit's restitution statement.
//   - the layout seeded from the template, then trimmed to two rooms;
//   - the entry captured in Tenure, signed by hand, issued (EE) and the lease
//     activated on it;
//   - a ten-day completion request decided (EC);
//   - notice, then the exit captured from the entry with one floor worse,
//     the keys' return and the new address, signed and issued (ES);
//   - the comparison, a wear grid, a deduction backed by a quote, and the
//     first restitution statement (RD).
// Dates are today in Europe/Paris: the windows and deadlines then hold.
test.describe.configure({ mode: 'serial' })

const iso = new Intl.DateTimeFormat('en-CA', { timeZone: 'Europe/Paris' }).format(new Date())
const fr = (d: string) => d.split('-').reverse().join('/')
const year = Number(iso.slice(0, 4))
let assetUrl = ''
let leaseUrl = ''

// profile creates the owner's profile when the phase 1 flows did not run first.
async function profile(page: Page): Promise<void> {
  await page.goto('/')
  const none = page.getByTestId('no-profile')
  const home = page.getByTestId('welcome').or(page.getByTestId('home'))
  await expect(none.or(home).first()).toBeVisible()
  if (!(await none.isVisible())) return
  await page.getByRole('link', { name: 'Créer mon profil' }).click()
  await page.locator('#legal_name').fill('Bailleur E2E')
  await page.locator('#address').fill('1 place Bellecour, 69002 Lyon')
  await page.getByRole('button', { name: 'Créer mon profil' }).click()
  await expect(page.getByRole('button', { name: 'Enregistrer' })).toBeVisible()
}

// signByHand signs a completed inventory on S15c, every party on one copy.
async function signByHand(page: Page, inventoryUrl: string): Promise<void> {
  await page.goto(`${inventoryUrl}/review`)
  await page.getByTestId('prepare-pdf').click()
  await expect(page.getByTestId('pdf-link')).toHaveAttribute('href', /^blob:/, pdfWait)
  const parties = page.getByTestId('sign-party')
  const n = await parties.count()
  expect(n).toBeGreaterThanOrEqual(2)
  for (let i = 0; i < n; i++) await parties.nth(i).getByTestId('pick').click()
  await page.getByTestId('file-signed').setInputFiles(pdf('edl-signe.pdf'))
  await page.getByTestId('wet-record').click()
  await expect(page.getByTestId('review-state')).toHaveText('Émis', pdfWait)
  await expect(page.getByTestId('issued-doc')).toBeVisible()
}

// assessAll gives every item of every room a condition, the works question
// answered where it is asked: as at entry for an exit, « Bon état » otherwise.
async function assessAll(page: Page, exit: boolean): Promise<void> {
  const rooms = page.getByTestId('section-room')
  for (let r = 0; r < (await rooms.count()); r++) {
    await rooms.nth(r).click()
    const items = page.getByTestId('item')
    for (let i = 0; i < (await items.count()); i++) {
      const it = items.nth(i)
      if (exit) await it.getByTestId('same-as-entry').click()
      else await it.getByTestId('condition').getByText('Bon état', { exact: true }).click()
      if (!exit && (await it.getByTestId('works').count())) await it.getByTestId('works').getByText('Oui', { exact: true }).click()
    }
  }
  await expect(page.getByTestId('not-saved')).toHaveCount(0)
}

test('the inventory lifecycle: layout, a lease, the entry captured, signed and issued, the lease activated', async ({ page }) => {
  const violations = await watchCSP(page)
  await profile(page)

  // The asset, its diagnostics and its layout, trimmed to the entry and the living room.
  await page.goto('/assets/new')
  await page.locator('#address').fill('7 quai Saint-Vincent, 69001 Lyon')
  await page.locator('#surface').fill('38')
  await page.getByTestId('save').click()
  await expect(page.getByTestId('asset-title')).toHaveText('7 quai Saint-Vincent, 69001 Lyon')
  assetUrl = page.url().replace(/\/(overview)?$/, '')
  await addDiagnostic(page, 'DPE', { dpe: 'D' })
  await addDiagnostic(page, 'État des risques (ERP)', { result: 'Aucun risque notable' })
  await addDiagnostic(page, 'Électricité', { result: 'Sans anomalie' })
  await addDiagnostic(page, 'Gaz', { notRequired: 'Pas d’installation de gaz' })
  await addDiagnostic(page, 'Plomb (CREP)', { notRequired: 'Immeuble construit après 1949' })
  await addDiagnostic(page, 'Amiante', { notRequired: 'Permis de construire après 1997' })

  await page.getByTestId('open-layout').click()
  await page.locator('#seed_bedrooms').fill('1')
  await page.getByTestId('seed-layout').click()
  const rooms = page.getByTestId('room')
  await expect(rooms.first()).toBeVisible()
  // Keep Entrée and Séjour: remove every other room, from the last.
  for (let i = (await rooms.count()) - 1; i >= 0; i--) {
    const label = await rooms.nth(i).getByTestId('room-label').inputValue()
    if (label !== 'Entrée' && label !== 'Séjour') await rooms.nth(i).getByTestId('room-remove').click()
  }
  await expect(rooms).toHaveCount(2)
  await page.getByTestId('save-layout').click()
  await expect(page.getByTestId('unsaved')).toHaveCount(0)

  // The lease: starting today, its deposit, its contract generated and signed.
  await page.goto(`${assetUrl}/leases`)
  await page.getByTestId('new-lease').click()
  await page.getByTestId('add-tenant').click()
  await page.locator('#tenant_names').fill('Hugo Lefèvre')
  await page.locator('#tenant_email').fill('hugo@example.invalid')
  await page.getByTestId('save-tenant').click()
  await expect(page.getByTestId('tenant-dialog')).toBeHidden()
  await page.locator('#start_on').fill(fr(iso))
  await page.locator('#start_on').press('Tab')
  await page.locator('#rent').fill('700')
  await page.locator('#provision').fill('60')
  await page.locator('#deposit').fill('700')
  await page.locator('#irl_quarter').fill(`${year - 1}-Q3`)
  await page.locator('#irl_value').fill('144,51')
  await page.getByTestId('save').click()
  await expect(page.getByTestId('lease-state')).toHaveText('brouillon')
  leaseUrl = page.url().replace(/\/(overview)?$/, '')
  await page.goto(`${leaseUrl}/guarantees`)
  await page.getByTestId('add-guarantee').click()
  await page.getByTestId('save-guarantee').click()
  await expect(page.getByTestId('guarantees')).toContainText('Dépôt de garantie')
  await page.getByTestId('generate').click()
  await confirm(page)
  await page.getByTestId('tab-contract').click()
  await page.getByTestId('file-signed').setInputFiles(pdf('contrat-signé.pdf'))
  await page.getByTestId('send-signed').click()
  await expect(page.getByText('contrat-signé.pdf')).toBeVisible()

  // The entry, captured in Tenure: scheduled, started, every item assessed,
  // one key, then completed.
  await page.getByTestId('tab-inventories').click()
  await page.getByTestId('add-inventory').click()
  await expect(page.getByTestId('inventory-mode')).toContainText('Le saisir dans Tenure')
  await page.getByTestId('create-capture').click()
  await expect(page).toHaveURL(/\/inventories\/[^/]+\/capture$/)
  const entryUrl = page.url().replace(/\/capture$/, '')
  await page.getByTestId('scheduled-at').fill(`${iso}T09:00`)
  await page.getByTestId('place').fill('Sur place')
  await page.getByTestId('schedule').click()
  await expect(page.getByTestId('capture-state')).toHaveText('Planifié')
  await page.getByTestId('start').click()
  await expect(page.getByTestId('capture-state')).toHaveText('En cours')
  await assessAll(page, false)
  await page.getByTestId('section-keys').click()
  await page.getByTestId('add-key').click()
  await page.getByTestId('key-opens').fill('porte d’entrée')
  await page.getByTestId('key-qty').locator('input').fill('2')
  await page.getByTestId('save-keys').click()
  await expect(page.getByTestId('form-error')).toHaveCount(0)
  await expect(page.getByTestId('blocking')).toHaveCount(0)
  await page.getByTestId('complete').click()
  await expect(page.getByTestId('capture-state')).toHaveText('Terminé')

  // Signed by both parties on one copy: the EE document is issued.
  await signByHand(page, entryUrl)

  // Nothing blocks the activation any more.
  await page.goto(leaseUrl)
  await expect(page.getByTestId('blockers-activation')).toHaveCount(0)
  await page.getByTestId('action-activate').click()
  await confirm(page)
  await expect(page.getByTestId('lease-state')).toHaveText('en cours')
  expect(violations).toEqual([])
})

test('the inventory lifecycle: a ten-day completion request decided into EC', async ({ page }) => {
  const violations = await watchCSP(page)
  await page.goto(`${leaseUrl}/inventories`)
  const plus10 = new Date(Date.parse(`${iso}T00:00:00Z`) + 10 * 86400000).toISOString().slice(0, 10)
  await expect(page.getByTestId('tab-ten-day')).toContainText(fr(plus10))
  await page.getByTestId('open-completions').click()
  await expect(page.getByTestId('window-ten-day')).toContainText('Ouvert')
  await page.getByTestId('start-request').click()
  const line = page.getByTestId('request-line').first()
  await line.getByTestId('line-item').click()
  await page.getByRole('option', { name: 'Séjour · Murs' }).click()
  await line.getByTestId('line-condition').getByText('État d’usage').click()
  await line.getByTestId('line-comment').fill('Trous de chevilles au-dessus du radiateur')
  await page.getByTestId('save-request').click()
  const c = page.getByTestId('completion')
  await expect(c).toHaveCount(1)
  await expect(c.getByTestId('decision')).toHaveText('À trancher')
  await c.getByTestId('completion-line').first().getByTestId('line-decide').getByText('Accepter').click()
  await c.getByTestId('save-decision').click()
  await expect(c.getByTestId('decision')).toHaveText('Acceptée', pdfWait)
  await expect(c.getByTestId('download-ec')).toBeVisible()
  expect(violations).toEqual([])
})

test('the inventory lifecycle: notice, the exit captured from the entry, signed and issued, then compared', async ({ page }) => {
  const violations = await watchCSP(page)
  // The tenant gives notice, ending today.
  await page.goto(leaseUrl)
  await page.getByTestId('action-notice').click()
  await page.locator('#notice_end').fill(fr(iso))
  await page.locator('#notice_end').press('Tab')
  await confirm(page)
  await expect(page.getByTestId('lease-state')).toHaveText('congé donné')

  // The exit, pre-filled from the entry: every item checked as at entry,
  // except the living room floor, now worse.
  await page.goto(`${leaseUrl}/inventories`)
  await page.getByTestId('add-inventory').click()
  await expect(page.locator('#inv_kind')).toContainText('de sortie')
  await page.getByTestId('create-capture').click()
  await expect(page).toHaveURL(/\/inventories\/[^/]+\/capture$/)
  const exitUrl = page.url().replace(/\/capture$/, '')
  await page.getByTestId('scheduled-at').fill(`${iso}T17:00`)
  await page.getByTestId('place').fill('Sur place')
  await page.getByTestId('schedule').click()
  await page.getByTestId('start').click()
  await expect(page.getByTestId('capture-state')).toHaveText('En cours')
  await expect(page.getByTestId('blocking')).toContainText('pas encore vérifié')
  await assessAll(page, true)
  await page.getByTestId('section-room').filter({ hasText: 'Séjour' }).click()
  const floor = page.getByTestId('item').filter({ hasText: 'Sol' }).first()
  await floor.getByTestId('condition').getByText('Mauvais état', { exact: true }).click()
  await floor.getByTestId('comment').fill('Lame de parquet fendue devant la fenêtre')
  await floor.getByTestId('comment').blur()
  await expect(page.getByTestId('not-saved')).toHaveCount(0)

  // The keys came back today; the tenant's new address.
  await page.getByTestId('section-observations').click()
  await page.locator('#head_keys').fill(fr(iso))
  await page.locator('#head_keys').press('Tab')
  await page.getByTestId('new-address').fill('22 rue de la République, 38000 Grenoble')
  await page.getByTestId('save-header').click()
  await expect(page.getByTestId('form-error')).toHaveCount(0)
  await expect(page.getByTestId('blocking')).toHaveCount(0)
  await page.getByTestId('complete').click()
  await expect(page.getByTestId('capture-state')).toHaveText('Terminé')

  await signByHand(page, exitUrl)

  // S15d: the floor alone is worse than at entry.
  await page.goto(`${exitUrl}/compare`)
  await expect(page.getByTestId('flagged')).toContainText('1 écart(s) à qualifier')
  await expect(page.getByTestId('compare-row')).toHaveCount(1)
  await expect(page.getByTestId('compare-row')).toContainText('Sol')
  await expect(page.getByTestId('result')).toHaveText('Dégradé')
  expect(violations).toEqual([])
})

test('the inventory lifecycle: a wear grid, a deduction backed by a quote, the RD statement', async ({ page }) => {
  const violations = await watchCSP(page)
  await page.goto(`${leaseUrl}/deposit`)
  await expect(page.getByTestId('deposit-held')).toHaveText(/700,00\s€/)
  await expect(page.getByTestId('flagged-item')).toHaveCount(1)
  await expect(page.getByTestId('flagged-item')).toContainText('Sol — Bon état → Mauvais état')
  await expect(page.getByTestId('missing-key')).toHaveCount(0)

  // A grid for floors: ten years, 10 % a year, 20 % residual value; annexed.
  await page.getByTestId('grid-new').click()
  await page.getByTestId('grid-name-input').fill('Grille du bailleur')
  await page.getByTestId('grid-source').fill('Grille annexée au bail')
  await page.locator('#gl_rate_0').fill('10')
  await page.locator('#gl_rate_0').blur()
  await page.locator('#gl_floor_0').fill('20')
  await page.locator('#gl_floor_0').blur()
  await page.getByTestId('grid-save').click()
  await expect(page.getByTestId('grid-name')).toContainText('Grille du bailleur')

  // The floor, worn for less than a year: the tenant's share is whole.
  await page.getByTestId('retain-item').click()
  await page.locator('#ded_cost').fill('350')
  await page.locator('#ded_cost').blur()
  await page.getByTestId('file-ded_doc').setInputFiles(pdf('devis-parquet.pdf'))
  await page.getByTestId('ded-save').click()
  await expect(page.getByTestId('deduction')).toHaveCount(1)
  await expect(page.getByTestId('deduction-share')).toHaveText('100 %')
  await expect(page.getByTestId('deduction-amount')).toHaveText(/350,00\s€/)

  // The first statement: 700 − 350, within two months of the keys.
  const pv = page.getByTestId('statement-preview')
  await expect(pv.getByTestId('pv-total')).toHaveText(/350,00\s€/)
  await pv.getByTestId('issue').click()
  await confirm(page)
  await expect(page.getByTestId('statement')).toHaveCount(1)
  await expect(page.getByTestId('statement')).toContainText(/350,00\s€/)
  await expect(page.getByTestId('download-rd')).toBeVisible()
  await expect(page.getByTestId('deposit-done')).toBeVisible()
  await expect(page.getByTestId('retain-item')).toHaveCount(0)

  // Every document of the lifecycle is in the archive, numbered in its series.
  await page.goto('/documents?class=inventory')
  const numbers = page.getByTestId('doc-number')
  for (const series of ['EE', 'EC', 'ES']) await expect(numbers.filter({ hasText: new RegExp(`^${series}-[0-9A-F]{8}-\\d{4}-\\d{4}$`) })).toHaveCount(1)
  await page.goto('/documents?class=restitution')
  await expect(page.getByTestId('doc-number').filter({ hasText: /^RD-[0-9A-F]{8}-\d{4}-\d{4}$/ })).toHaveCount(1)
  expect(violations).toEqual([])
})
