# Personal data breach (RGPD art. 33 and 34)

A breach is any loss of confidentiality, integrity or availability of personal data: a leaked
backup or key, unauthorised access to a scope, a document sent to the wrong person, or data
deleted without a backup. The **controller** is the owner (the bailleur). The operator of tenure
assists them and acts within hours.

**The 72-hour clock starts when the breach is known**, not when it is confirmed.

## Hour 0 to 4: contain

1. Open a private GitHub issue labelled `incident`, write down the time of discovery and keep
   a timeline in it. Write facts only.
2. Contain without destroying evidence:
   - revoke the leaked credentials: Socrate client secret, S3 keys, database passwords,
     `TENURE_SESSION_KEY` (rotating it ends every browser session);
   - revoke the grant concerned (`DELETE /grants/{id}`);
   - stop tenure if the leak is ongoing.
3. Preserve the evidence:
   - a copy of the Caddy access logs and the journal (`journalctl -u tenure`);
   - an export of the audit chain (`GET /audit/events.csv`);
   - run `tenure job audit.verify`.

## Hour 4 to 48: assess

4. Establish:
   - which owners, tenants and guarantors are affected and which data: identity, contact,
     income documents, bank details, rent arrears;
   - the period;
   - the cause;
   - whether the data was encrypted and its key safe.

   The audit chain and the access logs answer most of this.
5. Assess the risk to the people. Identity and income documents, bank details or arrears
   usually mean a high risk.

## Before hour 72: notify

6. Unless the breach is unlikely to result in a risk, notify the CNIL through its online
   notification service. If facts are missing, notify in phases. The notification gives:
   - the nature of the breach and the categories and approximate number of people and records;
   - the contact point;
   - the likely consequences;
   - the measures taken and planned.

   The notification is the owner's, as controller; the operator prepares it with them.
7. If the risk is high, inform the people affected without undue delay, in plain French: what
   happened, what it means for them, what was done, what they should do and whom to contact.
   Use the tenant's e-mail on file.

## After

8. Record the breach in the register (`docs/rgpd/registre-des-traitements.md`, section
   « Violations »), notified or not, with the facts, effects and measures (art. 33.5).
9. Hold a post-mortem in the issue. Each measure decided becomes its own issue, and the threat
   model is updated.
