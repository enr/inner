# Task: pinning di una versione precisa di Java nella sandbox

Contesto: una macchina host con più JDK installati (es. `/opt/jdk/jdk-21`, `/opt/jdk/jdk-26`).
Obiettivo: dentro la sandbox il comando `java` è in `$PATH` e punta esattamente alla versione desiderata.

La root host è già visibile read-only nella sandbox (`--ro-bind / /`, `internal/isolator/bwrap.go:93`),
quindi i JDK non richiedono mount: il problema è interamente `PATH`/`JAVA_HOME`. I task seguenti
rimuovono le frizioni individuate nel codice.

Ordine consigliato: **T4 → T2 → T3 → T7 → T1 → T5 → T6 → T8 → T9** (i primi sono piccoli e
indipendenti; T6 dipende in parte da T1; T8 dipende da T6 come modello; T9 integra le doc
di T6 e T8 e va fatto per ultimo).

---

## T1 — `[env] path_prepend`: meccanismo first-class per anteporre directory al PATH della sandbox

**Stato: fatto** (`internal/config/types.go`, `merge.go`, `loader.go`, `internal/isolator/bwrap.go`,
`internal/profile/validator.go`, `docs/content/profiles.md`, e relativi test).
Nota di implementazione: il calcolo del PATH base è stato estratto in due funzioni condivise
in `bwrap.go` — `basePathValue` (priorità: `set["PATH"]` presente anche se vuota →
PATH host se `PATH` è in `inherit`/`inherit_all` → default hard-coded) e
`sandboxPathBeforeShim` (applica `path_prepend` sopra la base), usate sia per l'emissione
standalone di `--setenv PATH` sia dal blocco shim, che vi antepone `/tmp/inner-shims:`.

**Tipo:** feature · **Size:** M

**Contesto.** Oggi per fissare una toolchain (es. un JDK preciso) bisogna riscrivere l'intero
`PATH` in `[env] set`, sfruttando il dettaglio non documentato che i valori passano per
`ExpandPath` (`internal/config/loader.go:518`) e che `${PATH}` si espande contro l'host.
Con `extends`, un profilo figlio che sovrascrive `PATH` deve conoscere quello del padre.

**Comportamento atteso.** Nuovo campo TOML:

```toml
[env]
path_prepend = ["/opt/jdk/jdk-21/bin"]
```

Il PATH effettivo nella sandbox diventa:
`<shim-dir se presente>:<path_prepend joinati con ":">:<PATH base>`.

**Implementazione.**

1. `internal/config/types.go`, struct `EnvConfig`: aggiungere
   ``PathPrepend []string `toml:"path_prepend"` `` con doc comment che ne descrive semantica e ordine.
2. `internal/config/merge.go` (sezione `[env]`, righe 72–91): merge con
   `meta.IsDefined("env", "path_prepend")` e unione senza duplicati in cui **le voci
   dell'overlay precedono quelle del base** (`mergeUnique(overlay.Env.PathPrepend, base.Env.PathPrepend)`),
   così il profilo più derivato vince nella risoluzione dei binari. Documentare questa scelta nel commento.
3. `internal/config/loader.go`, `toRunConfig` (righe 512–520): applicare `ExpandPath` a ogni
   elemento di `PathPrepend`, come già fatto per `Env.Set`.
4. `internal/isolator/bwrap.go`: estrarre il calcolo del PATH base in un'unica funzione,
   oggi duplicato/implicito nel blocco shim (righe 342–351):
   - base = `cfg.Env.Set["PATH"]` se presente; altrimenti `os.Getenv("PATH")` se `PATH` è in
     `cfg.Env.Inherit` o `cfg.Env.InheritAll` è true; altrimenti `/usr/local/bin:/usr/bin:/bin`.
   - se `len(cfg.Env.PathPrepend) > 0`: finale = `strings.Join(PathPrepend, ":") + ":" + base`,
     emesso come `--setenv PATH` **dopo** il loop di `Env.Set` (righe 231–238), così vince sul set esplicito.
   - il blocco shim antepone `/tmp/inner-shims:` a questo valore finale (gli shim devono restare primi).
5. `internal/profile/validator.go`: nuovo step — per ogni elemento di `path_prepend` espanso,
   se `os.Stat` fallisce con `IsNotExist` → `addWarning` (non error: il path potrebbe esistere
   solo in sandbox via mount).

**Casi limite.** Elementi vuoti in lista → errore di validazione; `path_prepend` senza `PATH`
né in `set` né in `inherit` → si usa il default hard-coded; interazione con
`inherit_all = true` → base = PATH host.

**Test.**

- `merge_test`: overlay+base → ordine "overlay prima", niente duplicati.
- `loader_test`: espansione `~` e `${VAR}` negli elementi.
- `bwrap_test`:
  - (a) solo `path_prepend` → `--setenv PATH` con prepend+PATH host;
  - (b) `path_prepend` + `Set["PATH"]` → prepend + valore set;
  - (c) `path_prepend` + shim dir → PATH inizia con `/tmp/inner-shims:` seguito dal prepend;
  - (d) senza `path_prepend` → comportamento identico a oggi (regressione).
- `validator_test`: warning su directory inesistente.

**Doc.** `docs/content/profiles.md` sezione `[env]`: nuovo campo, semantica di merge con
`extends`, esempio JDK pinning.

**Accettazione.** Con profilo contenente solo `path_prepend = ["/opt/jdk/jdk-21/bin"]` e
`inherit = [..., "PATH", ...]`, dentro la sandbox `command -v java` →
`/opt/jdk/jdk-21/bin/java` e il resto del PATH host è preservato. Tutti i test esistenti verdi.

---

## T2 — Espandere le variabili nei valori di `-e/--env` come nei profili

**Stato: fatto** (`cmd/inner/cmd_run.go`, `cmd/inner/cmd_run_test.go`,
`docs/content/commands.md`).

**Tipo:** fix di coerenza · **Size:** S

**Comportamento attuale.** I valori di `[env] set` nei profili passano per `config.ExpandPath`
(`internal/config/loader.go:518`); i valori del flag `-e KEY=VAL` no: in
`cmd/inner/cmd_run.go:440-449` il valore di `parseEnvVar` è assegnato raw a `rc.Env.Set[k]`.
Risultato: `-e 'PATH=/opt/jdk/bin:${PATH}'` (quoting singolo) mette la stringa letterale
`${PATH}` nella sandbox.

**Comportamento atteso.** Stessa semantica del profilo: in `cmd_run.go:448` applicare
`rc.Env.Set[k] = config.ExpandPath(v)`.

**Nota di design (decisione presa, non riaprire):** si sceglie la coerenza col profilo.
Valori contenenti `$` letterale non sono supportati né qui né in `[env] set` — va documentato,
non gestito con escaping.

**Test.** In `cmd_run_test.go`:

- (a) `-e 'X=${INNER_TEST_VAR}/sub'` con var host impostata → valore espanso;
- (b) `-e X=plain-value` → invariato;
- (c) `-e 'X=~/dir'` — attenzione: `ExpandPath` espande `~` solo a inizio stringa;
  verificare e asserire il comportamento reale.

**Doc.** `docs/content/commands.md`, help del flag (`cmd_run.go:866`): aggiornare in
`"Set env variable: KEY=VAL (~, $VAR and ${VAR} are expanded against the host environment)"`.

**Accettazione.** `inner run --dry-run -e 'JH=${HOME}/x'` mostra `--setenv JH /home/<user>/x`.

---

## T3 — Validator: warning su riferimenti a variabili host non definite in `[env] set`

**Stato: fatto** (`internal/config/expand.go`, `internal/profile/validator.go` e relativi test).
Nota di implementazione: `UndefinedVarRefs` non riporta mai `UID`, `workdir` e
`workspaces_path` — i primi due sono token risolti altrove (non da `os.Getenv`), il terzo
non è mai sostituito nei valori di `[env] set` oggi, quindi segnalarlo come "variabile host
non definita" sarebbe fuorviante.

**Tipo:** feature (validazione) · **Size:** S/M

**Comportamento attuale.** `ExpandPath` (`internal/config/expand.go:13`) usa `os.Getenv`
dentro `os.Expand`: una variabile non definita si espande a stringa vuota, in silenzio.
`set = { JAVA_HOME = "${INNER_JDK}" }` con `INNER_JDK` non impostata produce `JAVA_HOME=""`
e nessun segnale.

**Comportamento atteso.** `inner profile validate` (e ogni percorso che usa `profile.Validate`)
emette un warning per ogni variabile referenziata in un valore di `[env] set`
(e in `path_prepend`, se T1 è già mergiato) non definita nell'ambiente host.

**Implementazione.**

1. Nuova funzione in `internal/config/expand.go`:

   ```go
   // UndefinedVarRefs returns the names of $VAR/${VAR} references in s that are
   // not defined in the host environment. "UID" is always considered defined.
   func UndefinedVarRefs(s string) []string
   ```

   Implementata con `os.Expand` e una mapping func che registra i nomi per cui `os.LookupEnv`
   restituisce `ok == false` (scartando il valore di ritorno) — così il parsing dei nomi resta
   identico a quello dell'espansione reale. Trattare come "definiti" anche i token `workdir`
   e `workspaces_path`.
2. `internal/profile/validator.go`, nuovo step dopo lo step 2: per ogni `k, v` in `p.Env.Set`,
   per ogni nome restituito da `config.UndefinedVarRefs(v)`:
   `addWarning(fmt.Sprintf("[env] set %s references undefined host variable $%s (expands to empty string)", k, name))`.

**Livello: warning, non error** — un profilo parametrico può essere validato su una macchina
dove la variabile non serve.

**Test.** `expand_test.go`: casi `$FOO`, `${FOO}`, misti definiti/non, `$UID` mai segnalata,
stringa senza `$` → nil. `validator_test.go`: profilo con var non definita → un warning col
nome giusto; con var definita via `t.Setenv` → nessun warning.

**Accettazione.** `inner profile validate java` su macchina senza `INNER_JDK` stampa il
warning; con `INNER_JDK` esportata non stampa nulla.

---

## T4 — `[env] inherit`: non inoltrare come stringa vuota le variabili assenti sull'host

**Stato: fatto** (`internal/isolator/bwrap.go`, `internal/isolator/bwrap_test.go`).

**Tipo:** bugfix · **Size:** XS

**Comportamento attuale.** `internal/isolator/bwrap.go:226-228`:

```go
for _, key := range cfg.Env.Inherit {
    args = append(args, "--setenv", key, os.Getenv(key))
}
```

Se la variabile non esiste sull'host viene comunque emesso `--setenv KEY ""`: nella sandbox
risulta *impostata ma vuota*. Con `inherit = ["JAVA_HOME"]` (profili contrib
`java-maven`/`java-gradle`) e host senza `JAVA_HOME`, tool che distinguono unset da vuota
si comportano male.

**Comportamento atteso.** Variabile **non definita** sull'host → nessun `--setenv`
(resta unset in sandbox, dato `--clearenv`). Variabile **definita ma vuota** → inoltrata
vuota (comportamento invariato: `os.LookupEnv` con `ok == true`).

**Implementazione.** Sostituire con:

```go
for _, key := range cfg.Env.Inherit {
    if val, ok := os.LookupEnv(key); ok {
        args = append(args, "--setenv", key, val)
    }
}
```

**Test.** In `bwrap_test.go`:

- (a) var non definita in `Inherit` → nessuna tripla `--setenv KEY` negli args;
- (b) var definita a `""` via `t.Setenv` → `--setenv KEY ""` presente;
- (c) regressione sul caso normale (già coperto da `INNER_TEST_KEY`, riga 338).

**Accettazione.** `inner run --dry-run -p java-maven` su host senza `JAVA_HOME` non contiene
`--setenv JAVA_HOME`.

---

## T5 — Validator: warning per valori di `[env] set` che sono path assoluti inesistenti

**Stato: fatto** (`internal/profile/validator.go` e relativi test).
Nota di implementazione: la costruzione di `expandedMountDests` è stata spostata prima
(subito dopo lo step 2) ed è ora condivisa dal nuovo step 2d e dallo step 6 (capabilities),
eliminando la duplicazione. Un test preesistente di T3
(`TestValidate_envSetDefinedHostVar_noWarning`) usava un path fittizio (`/opt/jdk/jdk-21`)
non presente sulla macchina di test: è stato aggiornato per usare una directory reale
(`t.TempDir()`), dato che ora quel valore viene correttamente controllato anche da T5.

**Tipo:** feature (validazione) · **Size:** S

**Comportamento attuale.** Il validator (`internal/profile/validator.go`) verifica esistenza
di mount src/dest ed entrypoint, ma `set = { JAVA_HOME = "/opt/jdk/jdk-99" }` con directory
inesistente passa senza segnalazioni; l'errore emerge solo al primo comando in sandbox.

**Comportamento atteso.** Warning per i valori di `[env] set` che, dopo espansione, sembrano
path assoluti e non esistono sull'host.

**Regola precisa (per evitare falsi positivi):** un valore è candidato al check se e solo se,
dopo `ExpandPath`, TUTTE queste condizioni valgono:

1. inizia con `/`;
2. non contiene `://` (esclude `unix:///...`, URL);
3. non contiene `:` (esclude valori PATH-like multipli);
4. non contiene i token `${workspaces_path}` o `${workdir}` nel valore originale;
5. non coincide con il `dest` espanso di alcun mount del profilo (path che esistono solo in sandbox).

Se candidato e `os.Stat` fallisce con `IsNotExist` →
`addWarning(fmt.Sprintf("[env] set %s=%q: path does not exist on host", k, expanded))`.
Altri errori di `Stat` (permessi) → nessuna segnalazione. Sempre **warning**, mai error.

**Implementazione.** Nuovo step in `Validate` dopo il check dei mount, riusando la mappa
`expandedMountDests` già costruita per le capabilities (righe 173–178: spostarne la
costruzione prima, se serve).

**Test.** `validator_test.go`: JAVA_HOME inesistente → warning; JAVA_HOME esistente
(dir temporanea) → nessun warning; `DOCKER_HOST=unix:///...` → nessun warning; valore con
`:` (PATH-like) → nessun warning; valore uguale a un mount dest → nessun warning.

**Accettazione.** Profilo con `JAVA_HOME = "/opt/jdk/jdk-99"` → `inner profile validate`
stampa il warning; con path esistente non stampa nulla.

---

## T6 — Profilo contrib di esempio: JDK pinning (`java-21.toml`)

**Tipo:** docs/esempio · **Size:** XS · **Dipendenza:** meglio dopo T1 (usa `path_prepend`);
se T1 non è mergiato, usare la variante `${PATH}` indicata sotto.

**Contesto.** `contrib/profiles/java-maven.toml` e `java-gradle.toml` fanno solo
`inherit = ["JAVA_HOME"]`: non c'è alcun esempio che mostri come fissare una versione precisa
di Java, né garanzia che il `java` in PATH coincida con `JAVA_HOME`.

**Deliverable.** Nuovo file `contrib/profiles/java-21.toml`:

```toml
schema_version = "1"
name        = "java-21"
description = "Java shell pinned to JDK 21 (adjust the /opt/jdk path to your machine)"
extends     = "shell"

[env]
# Con T1 mergiato:
# path_prepend = ["/opt/jdk/jdk-21/bin"]
# set = { JAVA_HOME = "/opt/jdk/jdk-21" }
# Senza T1 (variante attuale — ${PATH} è espanso a load time col PATH dell'host):
set = { JAVA_HOME = "/opt/jdk/jdk-21", PATH = "/opt/jdk/jdk-21/bin:${PATH}" }

[verify.custom]
checks = [
  { name = "java resolves to pinned JDK", cmd = "test \"$(readlink -f \"$(command -v java)\")\" = \"$(readlink -f /opt/jdk/jdk-21/bin/java)\"", severity = "critical" },
  { name = "JAVA_HOME matches pinned JDK", cmd = "test \"$JAVA_HOME\" = /opt/jdk/jdk-21", severity = "high" },
]
```

Nel file finale tenere solo UNA delle due varianti `[env]`, secondo lo stato di T1;
il commento sull'altra va rimosso.

**Doc.**

- `docs/content/examples.md`: nuova sezione "Pinning a specific JDK version" che mostra il
  profilo, l'uso di `inner verify`, e l'override one-shot:
  `inner run -p java-21 -e JAVA_HOME=/opt/jdk/jdk-26 -e "PATH=/opt/jdk/jdk-26/bin:$PATH"`.
- `docs/content/profiles.md`: nella sezione `[env]`, documentare esplicitamente che i valori
  di `set` sono espansi contro l'ambiente host a load time (`~`, `$VAR`, `${VAR}`, `$UID`).

**Accettazione.** Su una macchina con `/opt/jdk/jdk-21`: `inner run -p java-21` →
`java -version` riporta 21; `inner verify -p java-21` passa entrambi i check custom.
Il profilo passa `inner profile validate`.

---

## T7 — Correggere il commento fuorviante su `[noop]` in `types.go`

**Stato: fatto** (`internal/config/types.go`). Verificato che `docs/content/profiles.md`
non riportava l'affermazione errata: la tabella di merge (righe 136-137) già descrive
correttamente `noop.block` come union e `noop.rewrite` come merge per chiave — nessuna
modifica necessaria lì.

**Tipo:** docs/chore · **Size:** XS

**Comportamento attuale.** `internal/config/types.go:107`:
*"A user-declared [noop] section replaces the built-in defaults entirely."*
Ma nel codice non esistono default built-in di noop (nessun `DefaultNoop`;
`internal/shim/builder.go` costruisce solo da ciò che è nel profilo), e con `extends`
il merge è **additivo**: `block` in union, `rewrite` con override per chiave
(`internal/config/merge.go`, sezione noop, righe ~144–162).

**Deliverable.**

1. Verificare il comportamento reale del merge noop leggendo `merge.go` e i test
   (`loader_test.go:664-668`).
2. Riscrivere il doc comment di `NoopConfig` in `types.go:104-111` in modo accurato, ad es.:
   *"There are no built-in noop defaults. With extends, block lists are unioned and rewrite
   maps are merged per key, with the child profile overriding the base."*
3. Allineare la sezione `[noop]` di `docs/content/profiles.md` se riporta la stessa
   affermazione errata (verificare con grep su "built-in" / "replaces").

**Accettazione.** Commento e doc descrivono il comportamento effettivamente implementato in
`merge.go`; nessun cambiamento di codice eseguibile in questo task.

---

## T8 — Profilo contrib di esempio: Node pinning (`node-22.toml`)

**Tipo:** docs/esempio · **Size:** XS · **Dipendenza:** stesso pattern di T6; usa
`path_prepend` se T1 è mergiato, altrimenti la variante `${PATH}`.

**Contesto.** Non esiste in `contrib/profiles/` alcun esempio per Node.js, né una guida su
come fissare una versione precisa quando l'host ha più installazioni Node (es.
`/opt/node/node-22`, o versioni gestite da nvm/mise sotto `~/.nvm/versions/node/v22.x.x`).
A differenza di Java, Node introduce due frizioni aggiuntive con il modello della sandbox:

1. `~/.npmrc` è nella lista delle risorse sensibili nascoste di default
   (`internal/isolator/bwrap.go`, entry `"npmrc"` tra `sensitive`, bind su `/dev/null`).
   Serve `allow = ["npmrc"]` in `[sandbox]` se il profilo deve autenticarsi su registry privati.
2. La root è montata `--ro-bind`, quindi `npm install -g` nella posizione di default del
   pacchetto Node fallisce (permission denied): serve un prefix in area scrivibile.

**Deliverable.** Nuovo file `contrib/profiles/node-22.toml`:

```toml
schema_version = "1"
name        = "node-22"
description = "Node shell pinned to Node 22 (adjust the /opt/node path to your machine)"
extends     = "shell"

[mounts]
"~/.npm" = { dest = "~/.npm", mode = "rw" }              # cache di npm
"~/.npm-global" = { dest = "~/.npm-global", mode = "rw" } # target di `npm install -g`

[env]
inherit = ["HOME", "USER", "TERM", "LANG", "SHELL"]
# Con T1 mergiato:
# path_prepend = ["/opt/node/node-22/bin", "~/.npm-global/bin"]
# set = { NPM_CONFIG_PREFIX = "~/.npm-global" }
# Senza T1 (variante attuale — ${PATH} è espanso a load time col PATH dell'host):
set = { PATH = "/opt/node/node-22/bin:~/.npm-global/bin:${PATH}", NPM_CONFIG_PREFIX = "~/.npm-global" }

[entrypoint]
history = [
  "npm install",
  "npm run dev",
  "npm test",
]

[verify.custom]
checks = [
  { name = "node resolves to pinned version", cmd = "test \"$(readlink -f \"$(command -v node)\")\" = \"$(readlink -f /opt/node/node-22/bin/node)\"", severity = "critical" },
  { name = "node major version is 22", cmd = "node --version | grep -q '^v22\\.'", severity = "critical" },
]
```

Nel file finale tenere solo UNA delle due varianti `[env]`, secondo lo stato di T1;
il commento sull'altra va rimosso. Non abilitare `allow = ["npmrc"]` di default: va
aggiunto solo da chi estende il profilo e ne ha effettivamente bisogno (principio least
privilege, coerente con gli altri profili contrib).

**Test.** `internal/profile/validator_test.go` (o un test dedicato ai profili contrib, se
esiste un meccanismo di lint automatico su `contrib/profiles/*.toml`): il profilo deve
passare `profile.Validate` senza error.

**Doc.** Vedi T9.

**Accettazione.** Su una macchina con `/opt/node/node-22`: `inner run -p node-22` →
`node --version` riporta `v22.x.x`; `inner verify -p node-22` passa entrambi i check custom;
`npm install -g <pkg>` funziona e installa in `~/.npm-global`. Il profilo passa
`inner profile validate`.

---

## T9 — Integrare la documentazione con la guida al multi-runtime pinning

**Tipo:** docs · **Size:** S · **Dipendenza:** dopo T6 e T8 (li referenzia entrambi); se T1
è mergiato, documentare anche `path_prepend`.

**Contesto.** Con due esempi contrib separati (Java, Node) manca ancora la parte più
richiesta in pratica: un'app con **più runtime insieme** (es. backend Java 21 + frontend
Node 22). Comporre due profili esistenti oggi richiede duplicare i contenuti in un terzo
file, perché `extends` in `internal/config/types.go` (`Profile.Extends string`) accetta un
solo profilo base, non una lista — questa limitazione va resa esplicita in doc, non
risolta qui (è materia di un eventuale task successivo su `extends` multiplo/mixin).

**Deliverable.**

1. `docs/content/examples.md`: nuova sezione "Pinning multiple runtimes (Java + Node)" con:
   - il profilo combinato esplicito (non tramite `extends` multiplo, che non esiste):

     ```toml
     schema_version = "1"
     name        = "app-fullstack"
     description = "JDK 21 backend + Node 22 frontend"
     extends     = "shell"

     [sandbox]
     network = true

     [mounts]
     "~/.m2"         = { dest = "~/.m2",         mode = "rw" }
     "~/.npm"        = { dest = "~/.npm",        mode = "rw" }
     "~/.npm-global" = { dest = "~/.npm-global",  mode = "rw" }

     [env]
     set = {
       JAVA_HOME = "/opt/jdk/jdk-21",
       NPM_CONFIG_PREFIX = "~/.npm-global",
       PATH = "/opt/node/node-22/bin:~/.npm-global/bin:/opt/jdk/jdk-21/bin:${PATH}",
     }
     ```

   - nota esplicita: "runtime diversi (Java, Node) non confliggono su PATH — basta
     anteporre entrambe le `bin/`; un conflitto reale esiste solo tra due versioni dello
     stesso runtime (es. due JDK), e va risolto a livello di build tool (Maven/Gradle
     toolchains, `.nvmrc`), non di sandbox."
   - link ai profili `java-21.toml` (T6) e `node-22.toml` (T8) come building block.
2. `docs/content/profiles.md`, sezione `[env]`: aggiungere un paragrafo "Combining multiple
   toolchains" che spiega il limite di `extends` singolo e rimanda a `examples.md` per il
   pattern del profilo combinato esplicito.
3. Verificare (grep su `docs/content/`) che non ci siano altri punti che presentano Java o
   Node come gli unici runtime supportati, e correggerli per coerenza.

**Non incluso in questo task:** implementare `extends` multiplo o una sezione `[runtimes]`
dichiarativa — sono estensioni possibili ma richiedono un task di design a sé (impatto su
`merge.go`, rilevamento cicli, e sulla semantica di override); qui ci si limita a
documentare il pattern che funziona oggi col codice esistente.

**Accettazione.** `docs/content/examples.md` contiene la sezione multi-runtime con un
profilo copiabile e funzionante; `docs/content/profiles.md` spiega il limite di `extends`
singolo; nessun cambiamento di codice Go in questo task.
