# Registre des activités de traitement (RGPD, art. 30)

**Responsable du traitement** : le propriétaire bailleur (nom, adresse, contact), qui gère ses
biens avec Tenure. Chaque propriétaire tient son propre registre ; ce modèle décrit les
traitements que Tenure réalise pour lui.
**Sous-traitant** : l’exploitant de l’instance Tenure (nom, contact), qui héberge et maintient
l’application.
**Délégué à la protection des données** : aucun n’est obligatoire pour un bailleur personne
physique ; le point de contact est le propriétaire.

Mise à jour : à chaque changement des données collectées, des destinataires, des
sous-traitants ou des durées de conservation.

## 1. Gestion locative (baux, garanties, états des lieux)

| Rubrique | Contenu |
| --- | --- |
| Finalités | Rédaction et exécution du bail ; gestion des garanties ; états des lieux ; diagnostics |
| Base légale | Exécution du contrat (art. 6.1.b) ; obligation légale (loi du 6 juillet 1989, art. 6.1.c) |
| Personnes concernées | Locataires, colocataires, garants |
| Données | Identité, coordonnées, date et lieu de naissance si le bail l’exige, pièces justificatives (identité, domicile, activité, ressources) dans la limite du décret n° 2015-1437, acte de cautionnement, état des lieux et ses photos (pièces, équipements, compteurs ; elles peuvent montrer les biens du locataire) |
| Destinataires | Le propriétaire ; ses co-propriétaires et son comptable s’il leur a ouvert un accès (le comptable n’a aucune pièce d’identité, donnée de garant, état des lieux ni photo) |
| Durée de conservation | Pendant le bail, puis 10 ans après sa fin (prescription et preuve) ; dossier d’un candidat écarté : supprimé après 3 mois, seule une trace datée « refusé » est conservée |

**Photos des états des lieux.** Prises par le propriétaire lors de l’état des lieux, elles sont
analysées par l’antivirus puis nettoyées avant archivage : toutes les métadonnées (EXIF,
localisation GPS, appareil, date de prise de vue) sont supprimées et l’image est réencodée.
Seule l’empreinte du fichier reçu est gardée, dans le journal d’audit. Elles sont archivées
sans modification possible, conservées comme l’état des lieux (10 ans après la fin du bail) et
consultées par un lien temporaire dont chaque émission est journalisée.

## 2. Quittancement, paiements, régularisation des charges

| Rubrique | Contenu |
| --- | --- |
| Finalités | Avis d’échéance, quittances, suivi des paiements et des impayés, révision IRL, régularisation annuelle des charges |
| Base légale | Exécution du contrat ; obligation légale (art. 21 et 23 de la loi de 1989) |
| Personnes concernées | Locataires, garants (en cas d’impayé) |
| Données | Montants et dates des paiements, mode de paiement, références bancaires des virements reçus, retards, relances |
| Destinataires | Le propriétaire ; co-propriétaires et comptable autorisés |
| Durée de conservation | 10 ans à compter de l’émission ou de la fin du bail, selon le document (paramètre `archive_retention_years`) |

## 3. Déclaration des revenus fonciers du propriétaire

| Rubrique | Contenu |
| --- | --- |
| Finalités | Synthèse annuelle (formulaire 2044, micro-foncier) |
| Base légale | Intérêt légitime et obligation fiscale du propriétaire |
| Personnes concernées | Le propriétaire et ses co-propriétaires ; les locataires apparaissent seulement par leurs loyers |
| Données | Loyers encaissés, charges déductibles, déficits reportés |
| Destinataires | Le propriétaire ; son comptable autorisé |
| Durée de conservation | 10 ans à compter de la fin de l’exercice |

## 4. Archivage et journal d’audit

| Rubrique | Contenu |
| --- | --- |
| Finalités | Preuve et intégrité des documents émis ; traçabilité de chaque modification |
| Base légale | Obligation légale de conservation ; intérêt légitime (sécurité, preuve) |
| Personnes concernées | Toutes les personnes ci-dessus ; les utilisateurs par leur identifiant Socrate |
| Données | Documents archivés (empreinte SHA-256, verrouillage 10 ans) ; journal des actions (qui, quoi, quand, valeurs avant et après), chaîné par hachage et non modifiable |
| Destinataires | Le propriétaire, son comptable (lecture du journal) |
| Durée de conservation | 10 ans ; au-delà, les documents sont listés pour revue manuelle ; aucune suppression automatique |

## 5. Authentification et sécurité

| Rubrique | Contenu |
| --- | --- |
| Finalités | Connexion par Socrate (fournisseur d’identité de l’exploitant), sessions du navigateur, prévention des abus |
| Base légale | Intérêt légitime (sécurité du service) |
| Personnes concernées | Propriétaires, co-propriétaires, comptables, administrateurs |
| Données | Identifiant Socrate (`sub`), heure et méthode d’authentification, session côté serveur, adresse IP dans les journaux techniques |
| Destinataires | L’exploitant |
| Durée de conservation | Session : jusqu’à la déconnexion ou l’expiration ; journaux techniques : 30 jours ; Tenure ne stocke aucun mot de passe |

## Sous-traitants et transferts

| Sous-traitant | Rôle | Localisation |
| --- | --- | --- |
| Scaleway SAS | Hébergement du serveur, stockage objet des documents et des sauvegardes | France (fr-par) |
| Fournisseur d’e-mail de l’exploitant (à préciser) | Envoi des avis et quittances | Union européenne |
| Yousign SAS (si la signature électronique est activée) | Signature électronique des états des lieux : nom, prénom et e-mail des signataires, le document à signer, la preuve de signature | France, Union européenne |

Aucun transfert hors de l’Union européenne. Aucun autre tiers ne reçoit de données des
locataires en phase 1. En phase 2, Yousign ne reçoit que ce qu’exige la signature d’un état
des lieux, et seulement si le bailleur l’active.

## Mesures de sécurité (art. 32)

- Chiffrement en transit (TLS 1.2+) et au repos (volume de la base, chiffrement côté serveur des
  buckets, sauvegardes chiffrées avec age, clé gardée hors ligne).
- Cloisonnement par propriétaire dans la base (row-level security) ; aucun jeton dans le
  navigateur ; double authentification exigée pour les actions sensibles (synthèse fiscale,
  accès, exports).
- Fichiers reçus analysés par antivirus ; documents émis verrouillés 10 ans ; journal d’audit
  chaîné, vérifié chaque semaine.
- Sauvegarde quotidienne, restauration testée deux fois par an ; procédure de violation de
  données sous 72 h (`deploy/runbooks/breach.md`).

## Violations de données (art. 33.5)

Chaque violation est inscrite ici, notifiée à la CNIL ou non.

| Date de découverte | Faits | Personnes et données concernées | Effets | Mesures | Notification CNIL / personnes |
| --- | --- | --- | --- | --- | --- |
| — | — | — | — | — | — |
