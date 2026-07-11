# Backlog issue — correzioni e miglioramenti

Lista unificata di issue pronte per essere aperte su GitHub, ricavata da:

- **`SECURITY_REVIEW.md`** — punti aperti #1–#11 (i due item marcati FIXED sono esclusi);
- **`NONO_COMPARISON.md`** — proposte S1–S7, F1–F5, P1–P4, U1–U6;
- `docs/tasks/java-pinning.md` — **escluso**: tutti i task T1–T9 risultano completati.

Le sovrapposizioni sono deduplicate (es. SR#1 ↔ S1, SR#2 ↔ S5). Ogni issue indica
tipo, priorità, dimensione stimata, fonti e dipendenze. Le issue "abilitanti"
(enabler) sbloccano altre voci della lista e sono segnalate come tali.

**Priorità**

| Livello | Significato |
|---|---|
| **P0** | Correggere subito: exploitabile o perde dati |
| **P1** | Alto impatto: pianificare a breve |
| **P2** | Reale ma con raggio d'azione limitato |
| **P3** | Miglioria / da valutare su richiesta |

**Ordine di lavoro consigliato:** ISS-01 → ISS-02 → ISS-03 → ISS-08 → ISS-17 →
ISS-05 → ISS-04 → ISS-06 → bug P2 (ISS-10…14) → ISS-18/19 → resto.
Razionale: prima i fix exploitabili e i quick win (P0), poi il sign-off manuale
già dovuto, poi i due enabler strutturali (run-ID/JSON e proxy di rete) che
sbloccano credenziali, audit e rollback.

---

## P0 — Sicurezza e correttezza da sistemare subito

### ISS-01 · Remote profiles: blocking consent + `--sha256` pinning
`security` · **P0** · Size M · Fonti: SECURITY_REVIEW #2, NONO_COMPARISON S5 · **Enabler** per ISS-23, ISS-27

Un profilo TOML scaricato da URL controlla l'intera sandbox (`network`,
`inherit_all`, `allow`, entrypoint) senza conferma né verifica di integrità:
un URL malevolo = esfiltrazione dei segreti host. Richiede: (a) conferma
bloccante che riassuma le impostazioni pericolose richieste dal profilo remoto
(non auto-accettata da `--yes`); (b) flag `--sha256 <hash>` con abort su
mismatch; (c) rifiuto di `inherit_all` da sorgente remota salvo opt-in separato.

*Acceptance:* test che un profilo remoto con `inherit_all`/`network=true` non
parte senza consenso esplicito; test che il mismatch di checksum interrompe il run.

### ISS-02 · `safe-rw` e copie delle capability seguono i symlink
`security` `bug` · **P0** · Size S · Fonte: SECURITY_REVIEW #3

`copyFile` (`cmd/inner/sandbox_claude.go`) usa `os.ReadFile` che dereferenzia i
symlink: un link piantato in `~/.claude/` (es. → `~/.ssh/id_rsa`) fa copiare il
*contenuto* del target dentro la sandbox, aggirando l'hiding dei path sensibili.
Fix: `Lstat` su ogni entry; i symlink si saltano (o si ricreano con
`Readlink`+`Symlink` senza dereferenziare).

*Acceptance:* test con symlink verso un file fuori dall'albero sorgente: il
contenuto non deve comparire nella copia.

### ISS-03 · Estendere la denylist dei path sensibili + test di regressione
`security` · **P0** · Size S · Fonte: SECURITY_REVIEW #1 (mitigazione a breve termine)

Quick win in attesa di ISS-04: aggiungere alla tabella `sensitive` di
`internal/isolator/bwrap.go` i path noti mancanti (`~/.config/gh`,
`~/.terraform.d`, `~/.m2/settings.xml`, `~/.config/helm`,
`~/.local/share/keyrings`, profili browser) con relative chiavi `allow` e check
di `inner verify`. Aggiungere un test che fallisca quando un path noto non è
coperto, per evitare che la lista marcisca in silenzio.

---

## P1 — Alto impatto

### ISS-04 · Modalità home isolata (filesystem allowlist)
`security` `feature` · **P1** · Size L · Fonti: SECURITY_REVIEW #1 (fix proprio), NONO_COMPARISON S1 · **Enabler** (rende marginale ISS-03)

Oggi tutto ciò che non è nella denylist è leggibile (cookie browser, `.env`,
interi `$HOME`). Invertire il modello per i profili agente: `[sandbox] home =
"isolated"` monta `--tmpfs $HOME` e rende visibili solo workdir, mount
espliciti e directory delle capability; i path di sistema restano ro-bind.
Comportamento attuale preservato come `home = "host-ro"` (default per una
release, poi flip per i profili agente).

*Acceptance:* e2e — con `home = "isolated"` un `cat ~/.config/gh/hosts.yml`
piantato fallisce; toolchain di sistema (`/usr`, `/etc`) ancora funzionanti.

### ISS-05 · Proxy di rete supervisionato con allowlist di domini
`security` `feature` · **P1** · Size L · Fonte: NONO_COMPARISON S2 · **Enabler** per ISS-06, ISS-18 (eventi rete), ISS-21 (`--allow-domain`)

La rete oggi è on/off e i profili agente richiedono `network = true` → rete
aperta verso ovunque. Nuovo modo `[sandbox.network] mode = "allowlist"`:
sandbox con `--unshare-net` + socket verso un proxy CONNECT nel processo
`inner` padre, che applica allowlist di domini, nega sempre gli endpoint
metadata cloud (`169.254.169.254`, link-local), risolve il DNS lato proxy
validando gli IP prima della connessione (anti DNS-rebinding), e richiede un
token di sessione per-run. Esporta `HTTP_PROXY`/`HTTPS_PROXY`
(+`NODE_USE_ENV_PROXY=1`).

*Acceptance:* con allowlist `["api.anthropic.com"]`, curl verso il dominio
permesso passa, verso altri domini e verso 169.254.169.254 riceve rifiuto;
`inner verify` riflette la policy.

### ISS-06 · Credential injection via proxy (i segreti non entrano in sandbox)
`security` `feature` · **P1** · Size L · Fonte: NONO_COMPARISON S3 · Dipende da: ISS-05

Sostituire l'esposizione raw (`allow = ["git-credentials"]`) con iniezione:
il proxy in modalità reverse inietta il token (da keyring/env/file host) negli
header verso l'upstream configurato, opzionalmente ristretto a glob
metodo+path (`POST:/repos/*/issues`); richieste fuori policy → 403 + evento di
audit. Anche senza endpoint-filtering in v1, "token mai su filesystem/env della
sandbox" è un salto di categoria.

*Acceptance:* l'agente completa una chiamata API autenticata senza che il token
sia leggibile dentro la sandbox (grep su env e filesystem); endpoint fuori
policy → 403.

### ISS-07 · Sign-off manuale PID-namespace/TUI su terminali reali
`task` `qa` · **P1** · Size S (manuale) · Fonte: SECURITY_REVIEW #9

Il codice `--unshare-pid` per i run interattivi è già merged e testato in
unit/dry-run; manca la verifica umana non automatizzabile: TUI reali (claude,
gemini, cursor) con render, Ctrl-C, resize; bash con paste multilinea e
history; conferma isolamento (`ls /proc | grep -c '^[0-9]'` piccolo). Passi
esatti e criteri già scritti in SECURITY_REVIEW #9. Rollback documentato:
`pid_namespace = false` nel profilo.

*Acceptance:* checklist del punto #9 completata su almeno una macchina reale;
esito annotato e voce chiusa in SECURITY_REVIEW.

### ISS-08 · Run-ID visibile + output `--json`
`ux` `feature` · **P1** · Size M · Fonti: NONO_COMPARISON U1, U2 · **Enabler** per ISS-18, ISS-19

Stampare il run ID (già generato in `internal/executor/runid.go`) a inizio/fine
run e accettarlo come argomento ovunque (`inner log <run-id>`, futuro
`inner rollback <run-id>`). Aggiungere `--json` a `verify`, `doctor`,
`profile list`, `log` e `run --dry-run` (le strutture dati esistono già:
`Report`, `RunConfig`). Prerequisito per automazione/CI e per indirizzare
audit e rollback.

*Acceptance:* ogni comando elencato emette JSON valido e stabile con `--json`;
il run ID stampato è riutilizzabile con `inner log`.

---

## P2 — Bug reali a raggio limitato

### ISS-09 · `parseMount` CLI rifiuta `safe-rw`/`tmpfs`
`bug` · **P2** · Size S · Fonte: SECURITY_REVIEW #4

I profili accettano 4 modi di mount, il flag `-m SRC:DEST[:MODE]` solo
`ro`/`rw`. Estendere il parser a `safe-rw` (e decidere/documentare `tmpfs`,
che non ha sorgente host). Messaggio d'errore con l'elenco completo dei modi.

*Acceptance:* table test su tutti i modi validi + uno invalido.

### ISS-10 · Rollback incompleto su fallimento parziale di `MkdirAll` nei workspace
`bug` · **P2** · Size S · Fonte: SECURITY_REVIEW #5

In `internal/workspace/manager.go` le directory da creare sono aggiunte alla
lista di rollback solo *dopo* il successo di `MkdirAll`: un fallimento a metà
catena lascia directory orfane. Registrare `dirsToCreate(dest)` in `created`
*prima* della chiamata.

*Acceptance:* test con mkdir che fallisce a metà catena → nessuna directory
residua.

### ISS-11 · `extractExpiresAt` itera una map → decisione token nondeterministica
`bug` · **P2** · Size S · Fonte: SECURITY_REVIEW #6

Con più oggetti annidati che portano un campo di scadenza, il valore restituito
varia tra run (ordine di iterazione map randomizzato) → prompt di unlock
"a volte sì a volte no". Scegliere deterministicamente (chiavi note in ordine
di priorità, o la scadenza più vicina).

*Acceptance:* unit test con due `expiresAt` annidati → stesso valore su molte
esecuzioni.

### ISS-12 · `checkUsrReadonly` fail-open (deduce read-only da una write fallita)
`bug` `security` · **P2** · Size S · Fonte: SECURITY_REVIEW #8

Un check di sicurezza che può solo "passare in silenzio" dà falsa assicurazione
(e il relativo test fallisce quando la suite gira da root). Leggere il flag
`ro` del mount da `/proc/mounts` o vincolare il check al contesto in-sandbox;
sistemare il test (skip con uid 0 o `UsrDir` iniettata realmente read-only).

*Acceptance:* il check riporta correttamente lo stato in-sandbox e la suite
passa anche da root.

### ISS-13 · Deny anche sul path canonico per i comandi bloccati
`security` · **P2** · Size S · Fonte: NONO_COMPARISON S6

Gli shim `[noop] block` vincono solo via PATH: `/bin/rm` resta invocabile.
Bindare lo shim anche sopra ogni path reale risolto del comando bloccato
(seguendo catene tipo `/bin → /usr/bin`). Documentare il gap residuo (copia
del binario altrove).

*Acceptance:* con `block = ["rm"]`, sia `rm` sia `/usr/bin/rm` falliscono con
il messaggio dello shim.

### ISS-14 · Audit log strutturato per run (NDJSON + redaction)
`feature` `security` · **P2** · Size M · Fonte: NONO_COMPARISON S4 · Dipende da: ISS-08

Registrare per ogni run un record NDJSON append-only: profilo risolto
post-merge, mount, chiavi `allow`, modalità rete, entrypoint, report di verify,
exit code, durata — con redaction best-effort dei segreti in argv/env. Doppio
uso: audit e metadati di riproducibilità. Hash chain sugli eventi come step 2
(economico); firma solo se emergerà un caso d'uso compliance. Con ISS-05 attivo,
loggare anche le richieste del proxy (`inner log show <run-id> --network`).

*Acceptance:* `inner log show <run-id> --json` restituisce il record completo;
un token passato in `-e` compare redatto.

### ISS-15 · Snapshot del workspace e rollback
`feature` · **P2** · Size L · Fonti: NONO_COMPARISON F1 + P3 (reflink) · Dipende da: ISS-08

Snapshot content-addressable dei soli mount workspace (unica superficie
scrivibile) prima/dopo il run: `--snapshot` o `[output] snapshot = true`,
store in `~/.local/state/inner/snapshots/<run-id>/`, reflink `FICLONE` su
btrfs/XFS con fallback a dedup per hash, esclusioni gitignore-aware, cap di
storage. Comandi: `inner rollback <run-id> [--diff|--restore]`. v0 economica
per repo git: registrare lo stato dirty pre-run e mostrare `git diff` post-run.

*Acceptance:* run che modifica/crea/cancella file → `--diff` li mostra,
`--restore` riporta il workspace allo stato pre-run.

### ISS-16 · Flag di policy one-off su `inner run`
`ux` · **P2** · Size S · Fonte: NONO_COMPARISON U3 · `--allow-domain` dipende da ISS-05

Permettere esperimenti senza editare TOML: `--allow <key>` (chiavi di
`ValidAllowKeys`), `--block <cmd>` (shim al volo), e — con ISS-05 —
`--allow-domain <dom>`. Stessa semantica dei campi profilo, priorità CLI.

*Acceptance:* `inner run --block rm …` blocca `rm` senza profilo dedicato;
`--dry-run` riflette i flag.

### ISS-17 · `inner profile explain <name>`: policy effettiva della catena `extends`
`ux` · **P2** · Size M · Fonte: NONO_COMPARISON U4

Vista orientata al profilo (non al run come `--dry-run`) del merge finale:
mount/env/allow/noop risolti, con indicazione del layer di provenienza di ogni
valore. Riusa il plumbing `Explain` delle capability. Rende banale il debug di
`extends`.

*Acceptance:* per un profilo con 2 livelli di `extends`, l'output attribuisce
correttamente ogni valore al layer che lo definisce.

### ISS-18 · Triage amichevole dei fallimenti bwrap
`ux` · **P2** · Size S · Fonte: NONO_COMPARISON U5

Mappare i fallimenti comuni (mount dest mancante, userns disabilitati,
quirk ptmx) a messaggi one-line azionabili che puntano a `inner doctor`,
invece del solo exit code di bwrap.

*Acceptance:* i tre casi citati producono messaggi specifici riconoscibili.

---

## P3 — Migliorie e polish

### ISS-19 · `quoteGitConfigValue` riscrive `\r` come `\n`
`bug` · **P3** · Size XS · Fonte: SECURITY_REVIEW #7

Case `'\r'` copia-incollato dal case `'\n'` in `internal/git/sanitizer.go`:
emettere `\r`. Test di round-trip.

### ISS-20 · `expandAliases` splitta sugli spazi ignorando le quote
`bug` · **P3** · Size S · Fonte: SECURITY_REVIEW #10

`strings.Fields` rompe alias con argomenti quotati multi-parola. Usare un
tokenizer shell-like (shlex). Test su alias con argomento quotato.

### ISS-21 · Ampliare i pattern di `checkEnvSecrets` e dichiararlo euristico
`security` · **P3** · Size XS · Fonte: SECURITY_REVIEW #11

Aggiungere `API_KEY`, `ACCESS_KEY`, `_KEY`, `PASSWD`, `AUTH` ai pattern;
ammorbidire il wording del check (euristica, non garanzia).

### ISS-22 · Filtro seccomp opzionale (defense-in-depth)
`security` · **P3** · Size M · Fonte: NONO_COMPARISON S7

bwrap accetta `--seccomp <fd>`: filtro conservativo di default (deny `ptrace`,
`process_vm_readv`, `keyctl`, `bpf`, `mount`, `open_by_handle_at`, …) con
opt-out da profilo. Valutare in parallelo Landlock via `go-landlock` (compone
con bwrap).

### ISS-23 · `inner profile search/add` su indice firmato
`feature` · **P3** · Size M · Fonte: NONO_COMPARISON F4 · Dipende da: ISS-01

Indice firmato (cosign/minisign) su `contrib/profiles`; `inner profile add
<name>` copia in `~/.config/inner/profiles/` verificando la firma;
`inner profile search` sull'indice. Via di crescita community senza registry
completo.

### ISS-24 · Tool policy incatenate via shim rientranti
`feature` `security` · **P3** · Size L · Fonte: NONO_COMPARISON F3

Terzo modo di shim: `[noop.sandbox] git = "git-only"` → lo shim re-invoca
`inner run -p git-only -- git "$@"` in una sandbox figlia più stretta.
Richiede nested userns (macchineria `nested-user-ns` già esistente). Scope v1:
narrowing per-comando, niente policy caller-based.

### ISS-25 · Sessioni detached (`inner ps/attach/stop`)
`feature` · **P3** · Size L · Fonte: NONO_COMPARISON F2

Workflow "N agenti in parallelo": v0 pragmatica = integrazione documentata
tmux/abduco o `inner ps`/`inner stop` su pidfile per run-ID; supervisor PTY
completo solo se la domanda si concretizza.

### ISS-26 · Cache della runtime detection
`performance` · **P3** · Size S · Fonte: NONO_COMPARISON P1

`runtime.Detect()` esegue/statta a ogni invocazione: cache in
`~/.cache/inner/` (path/versione bwrap, display server) invalidata per mtime.

### ISS-27 · Cache HTTP (ETag) per i profili remoti
`performance` · **P3** · Size S · Fonte: NONO_COMPARISON P2 · Affine a ISS-01

`If-None-Match` invece del re-download a ogni run; riduce anche la superficie
TOCTOU una volta attivo il pinning.

### ISS-28 · Check di `inner verify` in parallelo
`performance` · **P3** · Size S · Fonte: NONO_COMPARISON P4

`Checker.Run` è sequenziale e il solo dial timeout può costare 2 s:
parallelizzare con WaitGroup mantenendo l'ordine di output.

### ISS-29 · Onboarding: packaging + `inner init` interattivo
`ux` · **P3** · Size M · Fonte: NONO_COMPARISON U6

Pubblicazione su package repo (AUR, deb/rpm dalla pipeline release esistente);
`inner init` interattivo: scegli agente → genera profilo → esegue `doctor`.

### ISS-30 · Login OAuth sandboxed (osservazione)
`feature` · **P3** · Size L · Fonte: NONO_COMPARISON F5 · Dipende da: ISS-06

Lo stato di login di `claude` vive in `~/.claude`, oggi copiato in blocco
dalla capability. Da rivalutare quando esiste ISS-06: token nel keyring invece
che in file leggibili dall'agente.

---

## Riepilogo per priorità

| Priorità | Issue |
|---|---|
| **P0** | ISS-01 remote profile trust · ISS-02 symlink nelle copie · ISS-03 estensione denylist |
| **P1** | ISS-04 home isolata · ISS-05 proxy rete allowlist · ISS-06 credential injection · ISS-07 sign-off TUI/PID-ns · ISS-08 run-ID + `--json` |
| **P2** | ISS-09 parseMount · ISS-10 rollback workspace · ISS-11 extractExpiresAt · ISS-12 checkUsrReadonly · ISS-13 deny path canonico · ISS-14 audit log · ISS-15 snapshot/rollback · ISS-16 flag one-off · ISS-17 profile explain · ISS-18 triage bwrap |
| **P3** | ISS-19…ISS-30 |

### Grafo delle dipendenze (enabler)

```
ISS-01 ──► ISS-23, ISS-27
ISS-05 ──► ISS-06 ──► ISS-30
      └──► ISS-16 (--allow-domain), ISS-14 (eventi rete)
ISS-08 ──► ISS-14, ISS-15
ISS-04 ──► rende marginale ISS-03 (che resta il quick win immediato)
```
