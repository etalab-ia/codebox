# Runtime Microsandbox

## Téléchargement vérifié au premier démarrage

`just-code` embarque le SDK Go Microsandbox. Au premier `start`, il télécharge automatiquement la version correspondante du runtime depuis la [release amont](https://github.com/superradcompany/microsandbox/releases/tag/v0.7.14), une release immuable couverte par une attestation de release GitHub vérifiable, sous `$MSB_HOME` si cette variable est définie, sinon sous `~/.microsandbox/`. L'URL et l'empreinte SHA-256 attendue pour chaque plateforme sont gravées dans le binaire : l'archive est vérifiée avant toute décompression, puis le SDK contrôle encore la présence des fichiers et la version de `msb`. Le chemin géré n'a pas besoin d'être ajouté au `PATH`. `just-code doctor` reste en lecture seule : il vérifie un runtime déjà installé et indique comment l'installer s'il manque.

```bash
just-code doctor --microsandbox
```

## Fournir un runtime manuel (`MSB_PATH`)

Pour fournir un runtime installé et vérifié par un autre mécanisme (poste administré, cache interne ou environnement sans accès à GitHub), renseigne ensemble les deux chemins directs :

```bash
MSB_PATH=/chemin/vers/msb \
MSB_LIBKRUNFW_PATH=/chemin/vers/libkrunfw \
just-code doctor --microsandbox
```

`MSB_PATH` doit rapporter exactement `msb 0.7.14`. Dans ce mode manuel, `just-code` ne télécharge aucun artefact et la confiance dans les deux fichiers relève de leur mécanisme de provisionnement.

## Prérequis plateforme

Le téléchargement ne modifie pas la configuration de l'hôte. Sous Linux, KVM doit être accessible. Sous Windows, active **Windows Hypervisor Platform** dans les fonctionnalités Windows puis redémarre si elle ne l'est pas déjà.
