# RGPD

What Tenure keeps for the RGPD (RENT-SPEC-022, RENT-BE-001 §8b).

| File | Holds |
| --- | --- |
| `registre-des-traitements.md` | Records of processing (art. 30), kept by each owner as controller; template filled for Tenure's processing |
| `notice-locataires.md` | Privacy notice given to tenants and guarantors with the lease (art. 13 and 14) |
| `../../deploy/runbooks/breach.md` | Breach notification within 72 h (art. 33 and 34) |

Both documents are in French, the language of the owner and the tenant. They describe the
platform as built. A change in the data collected, a new processor or a new retention rule
updates them in the same PR. The retention periods come from `legal_params`
(`archive_retention_years`) and RENT-SPEC-005; these documents restate them, they do not set
them.

How a data subject exercises their rights (Q-93):

- **Owner, co-owner or accountant**: `GET /me/export` for their own data; the ZIP export for a
  portfolio's documents.
- **Tenant or guarantor**: on request to the owner. The owner exports the lease's documents
  (`POST /documents/export` by asset) and the tenant record. Correction goes through the API.
  Erasure waits for the end of retention, since lease documents and receipts are legal records.
